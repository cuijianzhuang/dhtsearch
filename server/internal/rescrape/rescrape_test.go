package rescrape

import (
	"context"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/scraper"
	"dhtsearch/server/internal/store"
)

func hash(b byte) string { return strings.Repeat(string(b), 40) }

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// fakeScraper records the hash lists it was asked to scrape and
// answers through fn (nil fn = every scrape round fails totally,
// so nothing is covered and nothing may be written).
type fakeScraper struct {
	mu    sync.Mutex
	calls [][]string
	fn    func(hashes []string) []scraper.SwarmStat
}

func (f *fakeScraper) Scrape(_ context.Context, hashes []string) []scraper.SwarmStat {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), hashes...))
	f.mu.Unlock()
	if f.fn == nil {
		return nil
	}
	return f.fn(hashes)
}

func (f *fakeScraper) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// zeroAnswers answers every asked hash with a covered all-zero
// observation — a genuine "tracker answered, nobody there" round.
func zeroAnswers(hashes []string) []scraper.SwarmStat {
	out := make([]scraper.SwarmStat, len(hashes))
	for i, h := range hashes {
		out[i] = scraper.SwarmStat{Hash: h}
	}
	return out
}

type fixture struct {
	st      *store.Store
	svc     *Service
	fake    *fakeScraper
	clock   time.Time
	sinkErr error
}

// newFixture wires a Service over an in-memory store with the
// same sink shape cmd/server uses (rounds land through
// UpsertSwarmStats, stamped with the fixture clock).
func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{st: st, fake: &fakeScraper{}, clock: time.Unix(1_800_000_000, 0)}
	if cfg.Logger == nil {
		cfg.Logger = quietLogger()
	}
	if cfg.Sink == nil {
		cfg.Sink = func(stats []scraper.SwarmStat) {
			rows := make([]store.SwarmStats, 0, len(stats))
			now := f.clock.Unix()
			for _, sw := range stats {
				rows = append(rows, store.SwarmStats{
					InfoHash: sw.Hash, Seeders: int64(sw.Seeders),
					Leechers: int64(sw.Leechers), Completed: int64(sw.Completed),
					ScrapedAt: now,
				})
			}
			if err := st.UpsertSwarmStats(context.Background(), rows); err != nil && f.sinkErr == nil {
				f.sinkErr = err
			}
		}
	}
	f.svc = New(st, f.fake, cfg)
	f.svc.now = func() time.Time { return f.clock }
	return f
}

func (f *fixture) seedTorrent(t *testing.T, h string, createdAt time.Time) {
	t.Helper()
	err := f.st.Upsert(store.Torrent{
		InfoHash: h, Name: "fixture " + h[:4], TotalSize: 1 << 30, FileCount: 1,
		Files:     []filter.File{{Path: "movie.mkv", Size: 1 << 30}},
		CreatedAt: createdAt.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) swarm(t *testing.T, h string) store.SwarmStats {
	t.Helper()
	m, err := f.st.SwarmStatsFor(context.Background(), []string{h})
	if err != nil {
		t.Fatal(err)
	}
	sw, ok := m[h]
	if !ok {
		t.Fatalf("no swarm row for %s", h)
	}
	return sw
}

func enabledCfg() Config {
	return Config{Enabled: true, MinAge: time.Hour, PerMin: 60}
}

// TestRoundZeroAdvancesStreak: an all-zero re-scrape round lands
// through the ingest write path and advances the zero streak —
// and the next round respects the min-age gate against the
// snapshot the first round just wrote.
func TestRoundZeroAdvancesStreak(t *testing.T) {
	f := newFixture(t, enabledCfg())
	ctx := context.Background()
	h := hash('a')
	f.seedTorrent(t, h, f.clock.Add(-40*24*time.Hour))
	f.fake.fn = zeroAnswers

	res, err := f.svc.Round(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Selected != 1 || res.Updated != 1 || res.Zero != 1 {
		t.Fatalf("round = %+v, want selected/updated/zero = 1/1/1", res)
	}
	if f.sinkErr != nil {
		t.Fatal(f.sinkErr)
	}
	sw := f.swarm(t, h)
	if sw.ZeroCount != 1 || sw.ZeroSince != f.clock.Unix() || sw.ScrapedAt != f.clock.Unix() {
		t.Fatalf("swarm = %+v, want a fresh zero streak of 1", sw)
	}

	// Ten minutes later the snapshot is younger than MinAge: the
	// round is idle and the streak does not move.
	f.clock = f.clock.Add(10 * time.Minute)
	res, err = f.svc.Round(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != "idle" || f.fake.callCount() != 1 {
		t.Fatalf("round = %+v after %d scrape calls, want idle with no new scrape", res, f.fake.callCount())
	}
}

// TestRoundPositiveResetsStreak: a round that finds seeders
// updates the counts and ends the zero streak.
func TestRoundPositiveResetsStreak(t *testing.T) {
	f := newFixture(t, enabledCfg())
	ctx := context.Background()
	h := hash('b')
	f.seedTorrent(t, h, f.clock.Add(-40*24*time.Hour))
	f.fake.fn = zeroAnswers
	if _, err := f.svc.Round(ctx); err != nil {
		t.Fatal(err)
	}
	f.clock = f.clock.Add(2 * time.Hour)
	f.fake.fn = func(hashes []string) []scraper.SwarmStat {
		return []scraper.SwarmStat{{Hash: h, Seeders: 7, Leechers: 2, Completed: 42}}
	}
	res, err := f.svc.Round(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 || res.Zero != 0 {
		t.Fatalf("round = %+v, want updated 1, zero 0", res)
	}
	sw := f.swarm(t, h)
	if sw.Seeders != 7 || sw.Leechers != 2 || sw.Completed != 42 || sw.ZeroCount != 0 || sw.ZeroSince != 0 {
		t.Fatalf("swarm = %+v, want positive counts and a cleared streak", sw)
	}
	if got := store.SwarmHealth(&sw); got != store.HealthAlive {
		t.Fatalf("health = %s, want alive", got)
	}
}

// TestRoundFailedWritesNothing: when every tracker fails, the
// round returns no snapshots, the sink never runs, an existing
// streak is untouched, and only the round counter moves.
func TestRoundFailedWritesNothing(t *testing.T) {
	f := newFixture(t, enabledCfg())
	ctx := context.Background()
	h := hash('c')
	f.seedTorrent(t, h, f.clock.Add(-40*24*time.Hour))
	f.fake.fn = zeroAnswers
	if _, err := f.svc.Round(ctx); err != nil {
		t.Fatal(err)
	}
	before := f.swarm(t, h)

	f.clock = f.clock.Add(2 * time.Hour)
	f.fake.fn = nil // total failure
	res, err := f.svc.Round(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Selected != 1 || res.Updated != 0 || res.Zero != 0 {
		t.Fatalf("round = %+v, want selected 1, updated 0, zero 0", res)
	}
	if after := f.swarm(t, h); after != before {
		t.Fatalf("swarm changed across a failed round: %+v -> %+v", before, after)
	}
	stats, err := f.st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats[StatRounds] != 2 || stats[StatUpdated] != 1 || stats[StatZeroRounds] != 1 {
		t.Fatalf("stats = rounds %d updated %d zero %d, want 2/1/1",
			stats[StatRounds], stats[StatUpdated], stats[StatZeroRounds])
	}
}

// TestRoundRateCap: a round never scrapes more than PerMin
// hashes, however many are eligible.
func TestRoundRateCap(t *testing.T) {
	cfg := enabledCfg()
	cfg.PerMin = 3
	f := newFixture(t, cfg)
	ctx := context.Background()
	for _, b := range []byte{'a', 'b', 'c', 'd', 'e'} {
		f.seedTorrent(t, hash(b), f.clock.Add(-40*24*time.Hour))
	}
	f.fake.fn = zeroAnswers
	res, err := f.svc.Round(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Selected != 3 {
		t.Fatalf("selected = %d, want the PerMin cap of 3", res.Selected)
	}
	if got := len(f.fake.calls[0]); got != 3 {
		t.Fatalf("scraper received %d hashes, want 3", got)
	}
}

// TestRoundQueueDeepBackoff: while the metadata fetch queue is
// at capacity the round is skipped wholesale — no selection, no
// scrape, no counters.
func TestRoundQueueDeepBackoff(t *testing.T) {
	cfg := enabledCfg()
	cfg.QueueDepth = func() int { return metadata.DefaultQueueCap }
	f := newFixture(t, cfg)
	ctx := context.Background()
	f.seedTorrent(t, hash('a'), f.clock.Add(-40*24*time.Hour))
	res, err := f.svc.Round(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != "queue_deep" {
		t.Fatalf("round = %+v, want skipped queue_deep", res)
	}
	if f.fake.callCount() != 0 {
		t.Fatalf("scraper called %d times during backoff, want 0", f.fake.callCount())
	}
	stats, err := f.st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats[StatRounds] != 0 {
		t.Fatalf("rescrape_rounds = %d after a skipped round, want 0", stats[StatRounds])
	}
}

// TestRoundDisabled: the master switch off means a pure no-op.
func TestRoundDisabled(t *testing.T) {
	cfg := enabledCfg()
	cfg.Enabled = false
	f := newFixture(t, cfg)
	f.seedTorrent(t, hash('a'), f.clock.Add(-40*24*time.Hour))
	res, err := f.svc.Round(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != "disabled" || f.fake.callCount() != 0 {
		t.Fatalf("round = %+v with %d scrape calls, want disabled no-op", res, f.fake.callCount())
	}
}

// TestRescrapeDrivesRetentionEligibility is the flywheel test:
// a torrent scraped only at ingest can never reach likely_dead,
// but re-scrape rounds alone — three all-zero rounds spanning
// more than 48 hours — accumulate exactly the evidence the
// retention rule consumes, and the torrent becomes eligible
// under the store's own eligibility check.
func TestRescrapeDrivesRetentionEligibility(t *testing.T) {
	cfg := enabledCfg() // MinAge 1h: rounds a day apart always re-select
	f := newFixture(t, cfg)
	ctx := context.Background()
	h := hash('d')
	f.seedTorrent(t, h, f.clock.Add(-40*24*time.Hour))
	f.fake.fn = zeroAnswers

	cutoff := f.clock.Add(-30 * 24 * time.Hour).Unix() // retention: 30-day min age
	count := func() int64 {
		n, err := f.st.CountLikelyDead(ctx, cutoff)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("eligible = %d before any re-scrape, want 0 (unknown health)", n)
	}
	for i := 0; i < 3; i++ {
		if _, err := f.svc.Round(ctx); err != nil {
			t.Fatal(err)
		}
		if f.sinkErr != nil {
			t.Fatal(f.sinkErr)
		}
		f.clock = f.clock.Add(25 * time.Hour)
	}
	sw := f.swarm(t, h)
	if sw.ZeroCount != 3 {
		t.Fatalf("zero_count = %d after 3 zero rounds, want 3", sw.ZeroCount)
	}
	if got := store.SwarmHealth(&sw); got != store.HealthLikelyDead {
		t.Fatalf("health = %s, want likely_dead", got)
	}
	if n := count(); n != 1 {
		t.Fatalf("eligible = %d after 3 zero rounds spanning 50h, want 1", n)
	}
}

// TestRunLoop: the scheduler fires rounds on its interval and
// stops with the context.
func TestRunLoop(t *testing.T) {
	f := newFixture(t, enabledCfg())
	f.svc.interval = 10 * time.Millisecond
	f.seedTorrent(t, hash('e'), f.clock.Add(-40*24*time.Hour))
	f.fake.fn = zeroAnswers
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.svc.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for f.fake.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if f.fake.callCount() == 0 {
		t.Fatal("run loop never scraped")
	}
}
