package retention

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/store"
)

// Fixture times: "now" floats with the wall clock (the runner
// computes its cutoff from time.Now), so ages are relative.
var (
	dayAgo  = func(n int) int64 { return time.Now().Add(-time.Duration(n) * 24 * time.Hour).Unix() }
	hourAgo = func(n int) int64 { return time.Now().Add(-time.Duration(n) * time.Hour).Unix() }
)

// seedDead plants a torrent whose swarm evidence derives to
// likely_dead: four consecutive zero scrapes, the streak spanning
// 71 hours. The streak is built through UpsertSwarmStats' own
// state machine, the way production rounds land.
func seedDead(t *testing.T, s *store.Store, hash string, createdAt int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.Upsert(store.Torrent{
		InfoHash: hash, Name: "fixture " + hash[:4], TotalSize: 1 << 30, FileCount: 1,
		Files: []filter.File{{Path: "movie.mkv", Size: 1 << 30}}, CreatedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}
	for _, ts := range []int64{hourAgo(72), hourAgo(48), hourAgo(24), hourAgo(1)} {
		if err := s.UpsertSwarmStats(ctx, []store.SwarmStats{
			{InfoHash: hash, Seeders: 0, Leechers: 0, ScrapedAt: ts},
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func seedAlive(t *testing.T, s *store.Store, hash string, createdAt int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.Upsert(store.Torrent{
		InfoHash: hash, Name: "fixture " + hash[:4], TotalSize: 1 << 30, FileCount: 1,
		Files: []filter.File{{Path: "movie.mkv", Size: 1 << 30}}, CreatedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSwarmStats(ctx, []store.SwarmStats{
		{InfoHash: hash, Seeders: 9, Leechers: 4, ScrapedAt: hourAgo(1)},
	}); err != nil {
		t.Fatal(err)
	}
}

func hashOf(b byte) string { return strings.Repeat(string(b), 40) }

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func staticSvc(s *store.Store, cfg Config) *Service {
	cfg.Logger = log.Default()
	return New(s, cfg)
}

// TestRunOnceDeletesAndPersists: a pass deletes exactly the
// eligible rows, persists the cumulative counter and the last-run
// record, and the status surface agrees with the stats table. A
// second pass finds nothing and does not inflate the counter.
func TestRunOnceDeletesAndPersists(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	dead1, dead2 := hashOf('a'), hashOf('b')
	seedDead(t, s, dead1, dayAgo(40))
	seedDead(t, s, dead2, dayAgo(90))
	seedAlive(t, s, hashOf('c'), dayAgo(40))
	seedDead(t, s, hashOf('d'), dayAgo(5)) // likely_dead but too young for 30d

	svc := staticSvc(s, Config{Enabled: false, MinAgeDays: 30, Interval: 24 * time.Hour})

	st, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.EligibleNow != 2 || st.DeletedTotal != 0 || st.LastRun != nil || st.Running {
		t.Fatalf("status before run = %+v, want 2 eligible, no runs yet", st)
	}

	info, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Deleted != 2 || info.EligibleRemaining != 0 || info.At <= 0 {
		t.Fatalf("run info = %+v, want 2 deleted, 0 remaining", info)
	}

	st, err = svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.DeletedTotal != 2 || st.LastRun == nil || st.LastRun.Deleted != 2 || st.EligibleNow != 0 {
		t.Fatalf("status after run = %+v", st)
	}
	for _, h := range []string{dead1, dead2} {
		got, err := s.AdminGet(ctx, h)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("torrent %s survived the pass", h)
		}
	}
	if n, err := s.Count(ctx); err != nil || n != 2 {
		t.Fatalf("torrents left = %d (err %v), want the alive and the young dead", n, err)
	}

	info, err = svc.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Deleted != 0 {
		t.Fatalf("second pass deleted %d, want 0", info.Deleted)
	}
	st, err = svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.DeletedTotal != 2 || st.LastRun.Deleted != 0 {
		t.Fatalf("status after second pass = %+v, want total still 2, last run 0", st)
	}
}

// TestRunOnceRespectsMinAge: the same evidence at a larger minimum
// age deletes nothing — the age gate is the operator's brake.
func TestRunOnceRespectsMinAge(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	seedDead(t, s, hashOf('a'), dayAgo(40))
	svc := staticSvc(s, Config{MinAgeDays: 60})
	info, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Deleted != 0 || info.EligibleRemaining != 0 {
		// EligibleRemaining is counted under the same 60d cutoff:
		// the hash is not eligible at all, so remaining is 0 too.
		t.Fatalf("run info = %+v, want nothing deleted and nothing eligible", info)
	}
	st, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.EligibleNow != 0 {
		t.Fatalf("eligible at 60d = %d, want 0", st.EligibleNow)
	}
}

// TestConcurrentRunRejected: the run slot is exclusive — RunOnce
// and Trigger both refuse while a pass is in flight.
func TestConcurrentRunRejected(t *testing.T) {
	s := openStore(t)
	svc := staticSvc(s, Config{MinAgeDays: 30})
	if !svc.claim() {
		t.Fatal("first claim refused")
	}
	if _, err := svc.RunOnce(context.Background()); !errors.Is(err, ErrRunning) {
		t.Fatalf("RunOnce during a run: err = %v, want ErrRunning", err)
	}
	if svc.Trigger() {
		t.Fatal("Trigger during a run returned true")
	}
	svc.release()
	if svc.Running() {
		t.Fatal("still running after release")
	}
}

// TestTriggerRunsInBackground: a trigger fires a real pass; once
// it lands, the outcome is visible through Status.
func TestTriggerRunsInBackground(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	seedDead(t, s, hashOf('a'), dayAgo(40))
	svc := staticSvc(s, Config{MinAgeDays: 30})
	if !svc.Trigger() {
		t.Fatal("Trigger refused with nothing in flight")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, err := svc.Status(ctx)
		if err == nil && st.DeletedTotal == 1 && !st.Running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, _ := svc.Status(ctx)
	t.Fatalf("triggered pass never landed: status = %+v", st)
}

// TestSchedulerHonoursEnabledGate: with the gate off the scheduler
// never deletes, however eligible the set; flipping it on lets the
// first pass run shortly after (startup delay shrunk for the test).
func TestSchedulerHonoursEnabledGate(t *testing.T) {
	s := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seedDead(t, s, hashOf('a'), dayAgo(40))

	var enabled atomic.Bool
	svc := New(s, Config{
		Logger: log.Default(),
		ConfigFunc: func() Config {
			return Config{Enabled: enabled.Load(), MinAgeDays: 30, Interval: time.Hour}
		},
	})
	svc.startupDelay = 20 * time.Millisecond
	svc.pollInterval = 10 * time.Millisecond
	go svc.Run(ctx)

	time.Sleep(200 * time.Millisecond)
	if n, err := s.Count(ctx); err != nil || n != 1 {
		t.Fatalf("disabled scheduler: torrents = %d (err %v), want 1 untouched", n, err)
	}

	enabled.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if n, err := s.Count(ctx); err == nil && n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("enabled scheduler never ran its first pass")
}
