// Package watchdog guards the ingest pipeline against its worst
// observed failure mode: a silent stall. In the incident that
// motivated this package the process ran for ~9 hours with the API
// healthy and not one error in the log, while ingest was frozen —
// discovery kept moving, nothing was stored, and only an unrelated
// upgrade restart brought it back. The fetch path parks that produce
// this shape (a metadata worker stuck inside the torrent client's
// process-wide lock, where no context can reach) cannot all be
// eliminated from outside the library, so this package provides the
// other three legs: detection, evidence, recovery.
//
//   - Every StatusEvery (10m) it logs one fixed-format line with the
//     pipeline counters' deltas, so any future stall is visible in
//     the journal at a glance (the incident's journal showed nothing
//     at all between startup lines and DHT noise).
//   - When discovery keeps moving but ingest makes zero progress for
//     WarnAfter (15m), it logs an ERROR and dumps the full goroutine
//     stacks — the evidence the incident never left.
//   - If the stall reaches ExitAfter (30m) it dumps the stacks again
//     and exits with code 1; the systemd unit's Restart=on-failure
//     brings the process back, which unwedges whatever was parked.
//   - A second detector covers the shape the zero-progress rule is
//     blind to: a crawl, where ingest keeps trickling (in the
//     2026-11-20 recurrence: +45 records per 10m against a ~330
//     baseline — every trickle reset the 15m zero-progress clock, so
//     the stall above never fired). Over each crawlWindow, if
//     discovery moved, the fetch queue did not drain, and stored
//     records grew by more than zero but less than crawlMinFetched,
//     it dumps stacks and logs an ERROR; a second consecutive crawl
//     window dumps again and exits as above. The queue-must-not-drain
//     condition is what keeps the legitimate post-restart backlog
//     plateau (workers chewing dead hashes while the queue falls)
//     from tripping it.
//   - When discovery is frozen too (whole-network outage shape), a
//     restart cannot help: it dumps stacks and warns, but never
//     exits.
package watchdog

import (
	"context"
	"log"
	"os"
	"runtime"
	"sync"
	"time"
)

// Probe is one sample of the pipeline's monotonic counters. All
// fields except QueueDepth are cumulative since process start.
type Probe struct {
	// Seen is the discovery-motion signal: infohashes harvested by
	// the DHT samplers. It grows whenever sampling works, regardless
	// of how many of the hashes turn out to be fetchable.
	Seen int64
	// Fetched is the ingest-progress signal: records actually stored
	// (post-filter upserts, the stats table's "fetched" counter).
	Fetched int64
	// TimedOut / Prescreen are fetch-attempt outcomes, used for the
	// periodic status line.
	TimedOut  int64
	Prescreen int64
	// QueueDepth is the metadata queue's current depth (0 when the
	// pipeline runs in FIFO-channel mode).
	QueueDepth int
}

// State is the watchdog's current verdict, surfaced through the API's
// crawler status so the admin overview can show it.
type State string

const (
	StateOK       State = "ok"
	StateStalled  State = "stalled"  // discovery moving, ingest frozen
	StateFrozen   State = "frozen"   // discovery and ingest both frozen
	StateDisabled State = "disabled" // watchdog off (setting or crawler off)
)

// Status is a point-in-time verdict for the API.
type Status struct {
	State State
	// Since is when the current non-ok episode began; zero when the
	// state is ok or disabled.
	Since time.Time
}

// Config controls a Watchdog. The zero value of every duration picks
// the documented default.
type Config struct {
	// Active gates the whole monitor: it only runs when the crawler
	// is enabled (a feeds-only deployment has no discovery signal to
	// compare ingest against).
	Active bool
	// Enabled, when non-nil, is read every tick — the settings
	// system's CRAWLER_WATCHDOG_ENABLED, hot. Nil means always on.
	Enabled func() bool
	// Probe samples the pipeline counters. An error skips the tick:
	// a failed sample must not masquerade as frozen counters.
	Probe func(ctx context.Context) (Probe, error)
	// Logger, nil for log.Default().
	Logger *log.Logger
	// Exit ends the process; nil for os.Exit. Injected in tests.
	Exit func(code int)
	// Stack returns the evidence dump; nil dumps all goroutines via
	// runtime.Stack. Injected in tests.
	Stack func() string
	// Now, nil for time.Now. Injected in tests.
	Now func() time.Time

	Tick        time.Duration // sample period, default 1m
	StatusEvery time.Duration // status line period, default 10m
	WarnAfter   time.Duration // stall warning threshold, default 15m
	ExitAfter   time.Duration // stall exit threshold, default 30m
}

// frozenRewarn is how often the both-frozen stack dump repeats. The
// first dump is the evidence; the repeats keep a multi-hour outage
// from looking like a one-off in the journal.
const frozenRewarn = time.Hour

// Crawl detection: the window over which throughput is judged, and
// the minimum stored-records growth that counts as healthy within
// it. crawlMinFetched is set at roughly a tenth of the observed
// healthy ingest baseline (~330 records per 10m with the production
// worker count): a window below it, with discovery moving and the
// queue not draining, is a pipeline whose workers are parked, not a
// quiet period — a quiet period drains its queue.
const (
	crawlWindow     = 10 * time.Minute
	crawlMinFetched = 30
)

type Watchdog struct {
	cfg    Config
	logger *log.Logger

	mu     sync.Mutex
	status Status

	startedAt      time.Time
	haveSample     bool
	last           Probe // previous tick's sample
	lastProgressAt time.Time
	seenAtProgress int64 // Seen value when Fetched last grew
	warned         bool  // stack dumped for the current stall episode
	frozenWarnedAt time.Time
	exited         bool

	statusBase Probe // sample the status-line deltas are measured from
	statusAt   time.Time

	// Crawl detection bookkeeping: crawlBase/crawlBaseAt open the
	// current evaluation window; crawlBadWindows counts consecutive
	// crawl windows (first dumps stacks, second exits).
	crawlBase       Probe
	crawlBaseAt     time.Time
	crawlStalled    bool
	crawlSince      time.Time
	crawlBadWindows int
}

// New builds a Watchdog; call Run to start it.
func New(cfg Config) *Watchdog {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	if cfg.Exit == nil {
		cfg.Exit = os.Exit
	}
	if cfg.Stack == nil {
		cfg.Stack = func() string {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			return string(buf[:n])
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Tick <= 0 {
		cfg.Tick = time.Minute
	}
	if cfg.StatusEvery <= 0 {
		cfg.StatusEvery = 10 * time.Minute
	}
	if cfg.WarnAfter <= 0 {
		cfg.WarnAfter = 15 * time.Minute
	}
	if cfg.ExitAfter <= 0 {
		cfg.ExitAfter = 30 * time.Minute
	}
	return &Watchdog{cfg: cfg, logger: cfg.Logger, status: Status{State: StateOK}}
}

// Status returns the current verdict. Safe to call from the API
// goroutine while Run is ticking.
func (w *Watchdog) Status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.cfg.Active || !w.enabled() {
		return Status{State: StateDisabled}
	}
	return w.status
}

func (w *Watchdog) enabled() bool {
	return w.cfg.Enabled == nil || w.cfg.Enabled()
}

func (w *Watchdog) setStatus(s Status) {
	w.mu.Lock()
	w.status = s
	w.mu.Unlock()
}

// Run samples the pipeline every Tick until ctx ends. It is a no-op
// when the watchdog is not Active.
func (w *Watchdog) Run(ctx context.Context) {
	if !w.cfg.Active {
		return
	}
	t := time.NewTicker(w.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		p, err := w.cfg.Probe(pctx)
		cancel()
		if err != nil {
			continue
		}
		w.Tick(w.cfg.Now(), p)
	}
}

// Tick advances the state machine with one sample. Run calls it once
// a minute; tests call it directly with synthetic times and probes.
// Side effects (status line, ERROR logs, stack dumps, exit) happen
// here, through the injected Logger/Stack/Exit.
func (w *Watchdog) Tick(now time.Time, p Probe) {
	if w.startedAt.IsZero() {
		w.startedAt = now
		w.lastProgressAt = now
		w.seenAtProgress = p.Seen
		w.statusBase = p
		w.statusAt = now
		w.crawlBase = p
		w.crawlBaseAt = now
	}
	if w.haveSample && p.Fetched > w.last.Fetched {
		// Any stored record is progress: the episode, if any, is over.
		w.lastProgressAt = now
		w.seenAtProgress = p.Seen
		w.warned = false
		w.frozenWarnedAt = time.Time{}
		w.setStatus(Status{State: StateOK})
	}
	w.last = p
	w.haveSample = true

	// Periodic status line: deltas since the previous line, in a
	// fixed one-line format the journal can be grepped for.
	if now.Sub(w.statusAt) >= w.cfg.StatusEvery {
		w.logger.Printf("pipeline: +%d seen +%d fetched queue_depth %d +%d timed_out +%d prescreen_dropped in %s",
			p.Seen-w.statusBase.Seen, p.Fetched-w.statusBase.Fetched, p.QueueDepth,
			p.TimedOut-w.statusBase.TimedOut, p.Prescreen-w.statusBase.Prescreen,
			now.Sub(w.statusAt).Round(time.Second))
		w.statusBase = p
		w.statusAt = now
	}

	if !w.enabled() {
		w.setStatus(Status{State: StateDisabled})
		return
	}

	// Crawl evaluation runs before the zero-progress logic: a
	// trickling ingest keeps lastProgressAt fresh, so that logic
	// would report ok forever without this.
	w.evalCrawl(now, p)

	stallFor := now.Sub(w.lastProgressAt)
	if stallFor < w.cfg.WarnAfter {
		if w.crawlStalled {
			w.setStatus(Status{State: StateStalled, Since: w.crawlSince})
		} else {
			w.setStatus(Status{State: StateOK})
		}
		return
	}

	if p.Seen > w.seenAtProgress {
		// The incident's shape: discovery is producing, ingest has
		// stored nothing for the whole window. Something downstream
		// of discovery is parked.
		w.setStatus(Status{State: StateStalled, Since: w.lastProgressAt})
		if !w.warned {
			w.warned = true
			w.logger.Printf("watchdog: ERROR ingest stalled: no records stored for %s while discovery kept moving (seen %d -> %d, queue_depth %d); goroutine dump follows",
				stallFor.Round(time.Second), w.seenAtProgress, p.Seen, p.QueueDepth)
			w.logger.Printf("watchdog: goroutine dump:\n%s", w.cfg.Stack())
		}
		if stallFor >= w.cfg.ExitAfter && !w.exited {
			w.exited = true
			w.logger.Printf("watchdog: ERROR ingest still stalled after %s; dumping goroutines and exiting for systemd to restart the process",
				stallFor.Round(time.Second))
			w.logger.Printf("watchdog: goroutine dump:\n%s", w.cfg.Stack())
			w.cfg.Exit(1)
		}
		return
	}

	// Discovery is frozen too: the shape of a network outage or a
	// dead DHT, where a restart buys nothing. Warn with evidence,
	// never exit.
	w.setStatus(Status{State: StateFrozen, Since: w.lastProgressAt})
	if w.frozenWarnedAt.IsZero() || now.Sub(w.frozenWarnedAt) >= frozenRewarn {
		w.frozenWarnedAt = now
		w.logger.Printf("watchdog: ERROR pipeline frozen: no discovery and no ingest for %s (seen %d, queue_depth %d); not exiting (a restart cannot fix an external outage); goroutine dump follows",
			stallFor.Round(time.Second), p.Seen, p.QueueDepth)
		w.logger.Printf("watchdog: goroutine dump:\n%s", w.cfg.Stack())
	}
}

// evalCrawl judges one crawlWindow of pipeline samples whenever a
// window has elapsed since the last evaluation, and acts on the
// verdict: first consecutive crawl window dumps the goroutine stacks
// with an ERROR, the second dumps again and exits for systemd to
// restart the process. A window is a crawl when all of these hold:
//
//   - discovery moved (Seen grew): a frozen discovery is the frozen
//     path's case, not this one's;
//   - ingest grew, but by less than crawlMinFetched: zero growth is
//     the zero-progress path's case (WarnAfter/ExitAfter above) and
//     keeps its existing timing untouched;
//   - the fetch queue is non-empty and did not shrink over the
//     window: workers that are merely chewing through a backlog of
//     dead hashes (the post-restart plateau) drain the queue even
//     when few records land, and a queue the workers cannot drain
//     is the parked-worker signature.
//
// In FIFO-channel mode QueueDepth is always 0, so the crawl detector
// never engages there; the zero-progress path still applies.
func (w *Watchdog) evalCrawl(now time.Time, p Probe) {
	if now.Sub(w.crawlBaseAt) < crawlWindow {
		return
	}
	seenDelta := p.Seen - w.crawlBase.Seen
	fetchedDelta := p.Fetched - w.crawlBase.Fetched
	queueStuck := p.QueueDepth > 0 && p.QueueDepth >= w.crawlBase.QueueDepth
	windowStart := w.crawlBaseAt
	w.crawlBase = p
	w.crawlBaseAt = now

	if seenDelta <= 0 || fetchedDelta <= 0 || fetchedDelta >= crawlMinFetched || !queueStuck {
		w.crawlStalled = false
		w.crawlBadWindows = 0
		return
	}
	if !w.crawlStalled {
		w.crawlStalled = true
		w.crawlSince = windowStart
	}
	w.crawlBadWindows++
	if w.crawlBadWindows == 1 {
		w.logger.Printf("watchdog: ERROR ingest crawling: only +%d records stored in %s while discovery kept moving (seen +%d) and the fetch queue did not drain (queue_depth %d); goroutine dump follows",
			fetchedDelta, crawlWindow, seenDelta, p.QueueDepth)
		w.logger.Printf("watchdog: goroutine dump:\n%s", w.cfg.Stack())
		return
	}
	if !w.exited {
		w.exited = true
		w.logger.Printf("watchdog: ERROR ingest still crawling after another %s (+%d records stored, queue_depth %d); dumping goroutines and exiting for systemd to restart the process",
			crawlWindow, fetchedDelta, p.QueueDepth)
		w.logger.Printf("watchdog: goroutine dump:\n%s", w.cfg.Stack())
		w.cfg.Exit(1)
	}
}
