// Package retention periodically deletes provably-dead torrents
// so the index sheds weight it can prove is dead weight.
//
// A torrent is deleted only when its swarm health derives to
// likely_dead through store.SwarmHealth (zero seeders AND zero
// leechers across at least 3 consecutive tracker scrapes spanning
// at least 48 hours — the same derivation the search API's
// hide-dead toggle uses) AND it was indexed longer ago than the
// configured minimum age. Everything else — alive, no_seed,
// suspect, unknown (never scraped), and the merely young — is
// untouchable, and the blocked table is never modified: a cleaned
// hash that resurfaces is re-ingested with a clean slate, while a
// blacklisted hash stays blacklisted.
//
// The runner is off by default (RETENTION_ENABLED): deletion is
// irreversible, so the operator reads the eligible count the
// status surface reports and opts in deliberately. A manual run
// from the admin panel is always available — it is an explicit
// operator action with its own confirmation, gated by the same
// minimum age.
package retention

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"dhtsearch/server/internal/store"
)

// Stats keys in the store's stats table: the cumulative counter is
// delta-written like every other pipeline counter; the last-run
// record is level-written (absolute values).
const (
	StatDeleted         = "retention_deleted"
	StatLastRunAt       = "retention_last_run_at"
	StatLastDeleted     = "retention_last_deleted"
	StatLastEligibleRem = "retention_last_eligible"
)

// Batch and ceiling bound one run's database footprint: deletes
// land in transactions of at most deleteBatch hashes so no single
// statement holds locks long on either backend, and one run stops
// after runCeiling deletions — whatever remains is the next
// scheduled run's work, never a reason to keep a run going for
// hours on a badly backed-up index.
const (
	deleteBatch = 1000
	runCeiling  = 50_000
)

// ErrRunning reports a run attempted while one is already in
// flight.
var ErrRunning = errors.New("retention: a cleanup run is already in flight")

// Config is the runner's configuration. With ConfigFunc set, a
// fresh Config is taken before every scheduling decision and every
// run, so settings saved through the admin API apply without a
// restart.
type Config struct {
	Enabled    bool
	MinAgeDays int
	Interval   time.Duration
	Logger     *log.Logger
	ConfigFunc func() Config
}

// RunInfo is one finished run's record, as persisted in the stats
// table and served by the status surface.
type RunInfo struct {
	At                int64 `json:"at"` // unix seconds the run finished
	Deleted           int64 `json:"deleted"`
	EligibleRemaining int64 `json:"eligible_remaining"`
}

// Status is the admin surface's view of the runner: the current
// configuration, the blast radius under it (EligibleNow), the
// cumulative outcome and the last run's record. LastRun is nil
// when no run has ever finished on this deployment.
type Status struct {
	Enabled      bool     `json:"enabled"`
	MinAgeDays   int      `json:"min_age_days"`
	IntervalH    int      `json:"interval_h"`
	Running      bool     `json:"running"`
	EligibleNow  int64    `json:"eligible_now"`
	DeletedTotal int64    `json:"deleted_total"`
	LastRun      *RunInfo `json:"last_run"`
}

// Service is the retention runner. Safe for concurrent use.
type Service struct {
	st    *store.Store
	cfg   Config
	cfgFn func() Config

	mu      sync.Mutex
	running bool
	rootCtx context.Context // set by Run; Trigger runs under it

	// Scheduling cadence, as fields so tests can shrink them.
	startupDelay time.Duration // first scheduled run happens this long after Run starts
	pollInterval time.Duration // how often the scheduler re-checks due-ness
}

// New builds a Service over the store.
func New(st *store.Store, cfg Config) *Service {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	s := &Service{
		st:           st,
		cfg:          cfg,
		cfgFn:        cfg.ConfigFunc,
		rootCtx:      context.Background(),
		startupDelay: 2 * time.Minute,
		pollInterval: time.Minute,
	}
	return s
}

func (s *Service) cfgNow() Config {
	cfg := s.cfg
	if s.cfgFn != nil {
		cfg = s.cfgFn()
	}
	if cfg.MinAgeDays <= 0 {
		cfg.MinAgeDays = 30
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 24 * time.Hour
	}
	if cfg.Logger == nil {
		cfg.Logger = s.cfg.Logger
	}
	return cfg
}

func (s *Service) logger() *log.Logger { return s.cfgNow().Logger }

// cutoff is the created_at boundary a torrent must predate to be
// eligible under cfg, evaluated at now.
func cutoff(cfg Config, now time.Time) int64 {
	return now.Unix() - int64(cfg.MinAgeDays)*24*3600
}

// Run is the scheduler loop: it wakes on a short poll, and runs a
// cleanup pass when the runner is enabled and a pass is due — the
// first one shortly after startup, then one per configured
// interval, counted from the last pass however it was started (a
// manual run resets the schedule). Disabled means the loop idles:
// no pass, no deletion, whatever the eligible set looks like.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.rootCtx = ctx
	s.mu.Unlock()

	started := time.Now()
	var lastAttempt time.Time // last failed attempt; successes persist their own record
	t := time.NewTicker(s.pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			cfg := s.cfgNow()
			if !cfg.Enabled {
				continue
			}
			// The next pass is due one interval after the last
			// finished one — the persisted last-run record is the
			// schedule's memory, so a manual run (or a previous
			// process's run) resets the schedule exactly like a
			// scheduled one — but never before this process's
			// startup delay has passed.
			due := started.Add(s.startupDelay)
			if stats, err := s.st.Stats(ctx); err == nil {
				if at := stats[StatLastRunAt]; at > 0 {
					if d := time.Unix(at, 0).Add(cfg.Interval); d.After(due) {
						due = d
					}
				}
			}
			if !lastAttempt.IsZero() {
				if d := lastAttempt.Add(cfg.Interval); d.After(due) {
					due = d
				}
			}
			if now.Before(due) {
				continue
			}
			if _, err := s.RunOnce(ctx); err != nil {
				if errors.Is(err, ErrRunning) {
					continue // a manual run is on it; its record resets the schedule
				}
				s.logger().Printf("retention: scheduled run: %v", err)
				// A failed pass still counts as the interval's
				// attempt, so a broken store is retried on the
				// configured cadence instead of every poll tick.
				lastAttempt = now
				continue
			}
		}
	}
}

// Trigger starts a cleanup pass in the background and reports
// whether it did: false means one is already in flight (the admin
// API answers 409). A manual pass ignores the Enabled gate — it is
// the operator's explicit instruction — but honours the configured
// minimum age like any other pass.
func (s *Service) Trigger() bool {
	s.mu.Lock()
	ctx := s.rootCtx
	s.mu.Unlock()
	if !s.claim() {
		return false
	}
	go func() {
		defer s.release()
		if _, err := s.execute(ctx); err != nil {
			s.logger().Printf("retention: manual run: %v", err)
		}
	}()
	return true
}

// RunOnce performs one full cleanup pass synchronously and returns
// its record. It does not consult the Enabled gate (the scheduler
// and Trigger own that decision); it returns ErrRunning when a
// pass is already in flight.
func (s *Service) RunOnce(ctx context.Context) (RunInfo, error) {
	if !s.claim() {
		return RunInfo{}, ErrRunning
	}
	defer s.release()
	return s.execute(ctx)
}

func (s *Service) claim() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func (s *Service) release() {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
}

// Running reports whether a pass is currently in flight.
func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// execute is one pass, with the run slot already claimed: drain
// the eligible set in bounded batches up to the per-run ceiling,
// then persist the outcome — the cumulative counter as a delta,
// the last-run record as absolute values — and log the one line
// per pass the operator greps for.
func (s *Service) execute(ctx context.Context) (RunInfo, error) {
	cfg := s.cfgNow()
	cut := cutoff(cfg, time.Now())
	var deleted int64
	cursor := ""
	for deleted < runCeiling {
		hashes, next, err := s.st.ScanLikelyDead(ctx, cut, cursor, deleteBatch)
		if err != nil {
			return RunInfo{}, err
		}
		if len(hashes) > 0 {
			n, err := s.st.DeleteRetentionBatch(ctx, hashes)
			if err != nil {
				return RunInfo{}, err
			}
			deleted += n
		}
		if next == "" {
			break
		}
		cursor = next
	}
	remaining, err := s.st.CountLikelyDead(ctx, cut)
	if err != nil {
		return RunInfo{}, err
	}
	info := RunInfo{At: time.Now().Unix(), Deleted: deleted, EligibleRemaining: remaining}
	if deleted > 0 {
		if err := s.st.IncrStatContext(ctx, StatDeleted, deleted); err != nil {
			return RunInfo{}, err
		}
	}
	for key, v := range map[string]int64{
		StatLastRunAt:       info.At,
		StatLastDeleted:     info.Deleted,
		StatLastEligibleRem: info.EligibleRemaining,
	} {
		if err := s.st.SetStatContext(ctx, key, v); err != nil {
			return RunInfo{}, err
		}
	}
	cfg.Logger.Printf("retention: deleted %d (eligible %d)", info.Deleted, info.EligibleRemaining)
	return info, nil
}

// Status assembles the admin surface's view: configuration from
// the settings layer, the eligible count under it (a live walk of
// the same candidate path a pass deletes from), and the persisted
// outcome record.
func (s *Service) Status(ctx context.Context) (Status, error) {
	cfg := s.cfgNow()
	eligible, err := s.st.CountLikelyDead(ctx, cutoff(cfg, time.Now()))
	if err != nil {
		return Status{}, err
	}
	stats, err := s.st.Stats(ctx)
	if err != nil {
		return Status{}, err
	}
	out := Status{
		Enabled:      cfg.Enabled,
		MinAgeDays:   cfg.MinAgeDays,
		IntervalH:    int(cfg.Interval / time.Hour),
		Running:      s.Running(),
		EligibleNow:  eligible,
		DeletedTotal: stats[StatDeleted],
	}
	if at := stats[StatLastRunAt]; at > 0 {
		out.LastRun = &RunInfo{
			At:                at,
			Deleted:           stats[StatLastDeleted],
			EligibleRemaining: stats[StatLastEligibleRem],
		}
	}
	return out, nil
}
