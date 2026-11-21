// Package rescrape periodically re-checks the swarm health of
// torrents already in the index.
//
// A torrent's swarm snapshot (seeders/leechers, see store's
// swarm_stats) is written exactly once, when the torrent is
// discovered and ingested. Nothing refreshes it afterwards, so
// every surface fed by the snapshot — seeder sorting, the
// five-level health derivation — runs on data that only gets
// staler, and the retention cleanup (internal/retention) can
// almost never prove a torrent dead: likely_dead needs at least
// three consecutive all-zero scrape rounds spanning 48 hours,
// and a single lifetime scrape is none of those. This runner is
// the flywheel that closes the loop: it re-scrapes the stalest
// snapshots (and torrents never scraped at all) through the same
// tracker scrape and the same write path ingest uses, so health
// evidence keeps accumulating for as long as a torrent sits in
// the index.
//
// It is deliberately a background citizen. It only ever talks to
// trackers (never the metadata fetch pool, never the admission
// gate — its subjects are already stored), it scrapes at a low
// fixed rate, and it skips its round entirely while the metadata
// fetch queue is full, so it can never compete with ingest for
// the one resource ingest is short of.
package rescrape

import (
	"context"
	"log"
	"time"

	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/scraper"
	"dhtsearch/server/internal/store"
)

// Stats keys in the store's stats table, delta-written like every
// other pipeline counter (see the stats mirror in cmd/server).
const (
	StatRounds     = "rescrape_rounds"      // rounds that scraped at least one hash
	StatUpdated    = "rescrape_updated"     // snapshots written (hashes a tracker answered for)
	StatZeroRounds = "rescrape_zero_rounds" // of those, all-zero snapshots — the rounds that advance zero_count
)

// Defaults for Config fields left at zero. They mirror the
// settings registry defaults; cfgNow applies them so a zero Config
// is the documented behaviour, not an accident.
const (
	defaultMinAge = 48 * time.Hour
	defaultPerMin = 60
)

// maxPerRound bounds one round's selection whatever the settings
// say, so a fat-fingered rate cannot turn a round into a burst
// the size of the index. The registry caps RESCRAPE_PER_MIN at
// the same value.
const maxPerRound = 10000

// Round cadence: one round per interval. The per-minute rate cap
// is the round's selection budget, so the cap holds by
// construction — a round never scrapes more than PerMin hashes,
// and rounds never overlap.
const roundInterval = time.Minute

// Scraper runs one scrape round over the given hashes and returns
// the round's aggregated snapshots in the ingest sink's shape.
// *scraper.Rescraper satisfies it; tests substitute a fake.
type Scraper interface {
	Scrape(ctx context.Context, hashes []string) []scraper.SwarmStat
}

// Config is the runner's configuration. With ConfigFunc set, a
// fresh Config is taken before every round, so settings saved
// through the admin API apply without a restart.
type Config struct {
	Enabled bool
	MinAge  time.Duration
	PerMin  int
	Logger  *log.Logger
	// Sink persists a round's snapshots. It is the same write
	// path the ingest scraper's sink uses (wired in cmd/server),
	// so a re-scrape round lands exactly like an ingest round.
	Sink func([]scraper.SwarmStat)
	// QueueDepth reports the metadata fetch queue's current
	// depth; nil means the queue does not exist (FIFO mode) and
	// counts as zero. A round is skipped while the depth is at
	// the queue's capacity — the queue full and shedding pushes
	// is ingest's saturation signal, and the one moment this
	// runner must add no load at all.
	QueueDepth func() int
	ConfigFunc func() Config
}

// Result is one round's outcome. Skipped is "" when the round
// ran, else the reason: "disabled", "queue_deep" or "idle"
// (nothing was eligible).
type Result struct {
	Skipped  string
	Selected int
	Updated  int
	Zero     int
}

// Service is the re-scrape runner. Safe for concurrent use.
type Service struct {
	st    *store.Store
	scr   Scraper
	cfg   Config
	cfgFn func() Config

	// Cadence and clock, as fields so tests can shrink them.
	interval time.Duration
	now      func() time.Time
}

// New builds a Service over the store and scrape engine.
func New(st *store.Store, scr Scraper, cfg Config) *Service {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &Service{
		st:       st,
		scr:      scr,
		cfg:      cfg,
		cfgFn:    cfg.ConfigFunc,
		interval: roundInterval,
		now:      time.Now,
	}
}

func (s *Service) cfgNow() Config {
	cfg := s.cfg
	if s.cfgFn != nil {
		cfg = s.cfgFn()
	}
	if cfg.Sink == nil {
		cfg.Sink = s.cfg.Sink
	}
	if cfg.QueueDepth == nil {
		cfg.QueueDepth = s.cfg.QueueDepth
	}
	if cfg.Logger == nil {
		cfg.Logger = s.cfg.Logger
	}
	if cfg.MinAge <= 0 {
		cfg.MinAge = defaultMinAge
	}
	if cfg.PerMin <= 0 {
		cfg.PerMin = defaultPerMin
	}
	if cfg.PerMin > maxPerRound {
		cfg.PerMin = maxPerRound
	}
	return cfg
}

// Run is the round loop: one round per interval until ctx ends.
// A round's own gates (enabled, queue depth, eligibility) decide
// whether it does anything; errors are logged and the loop
// carries on — a broken round must never take the runner down.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			res, err := s.Round(ctx)
			if err != nil {
				s.cfgNow().Logger.Printf("rescrape: round: %v", err)
				continue
			}
			switch res.Skipped {
			case "queue_deep":
				s.cfgNow().Logger.Printf("rescrape: skipped round: metadata fetch queue is full (depth %d)", s.queueDepth())
			case "":
				s.cfgNow().Logger.Printf("rescrape: selected %d, updated %d (zero %d)", res.Selected, res.Updated, res.Zero)
			}
		}
	}
}

func (s *Service) queueDepth() int {
	if s.cfg.QueueDepth == nil {
		return 0
	}
	return s.cfg.QueueDepth()
}

// Round performs one re-scrape round synchronously and persists
// its counters. The gates, in order: disabled, fetch queue full,
// nothing eligible. A round whose scrapes all fail still counts
// as a round — it ran — but writes nothing and updates nothing:
// no data is never written as zero.
func (s *Service) Round(ctx context.Context) (Result, error) {
	cfg := s.cfgNow()
	if !cfg.Enabled {
		return Result{Skipped: "disabled"}, nil
	}
	if cfg.QueueDepth != nil && cfg.QueueDepth() >= metadata.DefaultQueueCap {
		return Result{Skipped: "queue_deep"}, nil
	}
	cutoff := s.now().Add(-cfg.MinAge).Unix()
	hashes, err := s.st.StalestSwarmHashes(ctx, cutoff, cfg.PerMin)
	if err != nil {
		return Result{}, err
	}
	if len(hashes) == 0 {
		return Result{Skipped: "idle"}, nil
	}

	stats := s.scr.Scrape(ctx, hashes)
	res := Result{Selected: len(hashes), Updated: len(stats)}
	for _, sw := range stats {
		if sw.Seeders == 0 && sw.Leechers == 0 {
			res.Zero++
		}
	}
	if len(stats) > 0 && cfg.Sink != nil {
		cfg.Sink(stats)
	}

	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for key, delta := range map[string]int64{
		StatRounds:     1,
		StatUpdated:    int64(res.Updated),
		StatZeroRounds: int64(res.Zero),
	} {
		if delta == 0 {
			continue
		}
		if err := s.st.IncrStatContext(wctx, key, delta); err != nil {
			return res, err
		}
	}
	return res, nil
}
