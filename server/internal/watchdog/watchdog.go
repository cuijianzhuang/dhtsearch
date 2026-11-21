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
//   - Episode transitions are reported through Config.OnEvent
//     (begin once per episode, whichever detector fired first;
//     recovery when progress resumes) and Config.BeforeExit (a
//     synchronous flush hook immediately before an escalation
//     exit), so the alerting layer (internal/alerts) can notify
//     the operator externally without owning a second detector
//     that could disagree with this one.
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

// EventKind identifies an episode transition the alerting layer
// (internal/alerts) turns into an external notification. The
// watchdog owns detection; the events are its only outward signal,
// so an alert can never disagree with the watchdog's own verdict.
type EventKind string

const (
	// EventStalled: discovery moving, ingest zero-progress for
	// WarnAfter — the incident's shape.
	EventStalled EventKind = "stalled"
	// EventCrawling: ingest trickling far below baseline with the
	// fetch queue not draining (first crawl window of an episode).
	EventCrawling EventKind = "crawling"
	// EventFrozen: discovery and ingest both frozen (outage shape;
	// warns and alerts, never exits).
	EventFrozen EventKind = "frozen"
	// EventRecovered: ingest progress resumed after an alerted
	// episode.
	EventRecovered EventKind = "recovered"
	// EventExiting: the watchdog is about to exit the process for
	// a systemd restart; delivered through BeforeExit, not OnEvent.
	EventExiting EventKind = "exiting"
)

// Event is one episode transition, with the counters the alert
// message quotes. Since is when the episode began (for a recovery,
// the start its begin event reported); StalledFor is the
// episode's age at the transition.
type Event struct {
	Kind           EventKind
	Since          time.Time
	StalledFor     time.Duration
	Probe          Probe // latest sample at the transition
	SeenAtProgress int64 // Seen when Fetched last grew (stall kinds)
	WindowFetched  int64 // records stored in the judged window (crawl kind)
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
	// OnEvent, when non-nil, is called synchronously from Tick on
	// episode transitions: a stall/crawl/frozen episode begins
	// (once per episode) and an alerted episode recovers. It must
	// be fast and non-blocking — it only hands the event to the
	// alerting layer, which owns delivery.
	OnEvent func(Event)
	// BeforeExit, when non-nil, is called synchronously immediately
	// before Exit on a stall/crawl escalation, so the alerting
	// layer can flush the episode's alert (and send its exit
	// notice) before the process dies. It is expected to bound its
	// own wait; Tick does not return to its caller until it does.
	BeforeExit func(Event)

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

	// Alerting bookkeeping: alerted is true from the episode's
	// first OnEvent emission until its recovery emission, so each
	// episode produces exactly one begin event whichever detector
	// (zero-progress, crawl, frozen) fired first, and at most one
	// recovery. alertSince is the episode start the begin event
	// reported, reused to age the recovery.
	alerted    bool
	alertSince time.Time

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

// emitBegin fires the episode's single begin event. Callers pass
// the detector-specific kind and episode start; the guard here is
// what makes the three detectors share one episode.
func (w *Watchdog) emitBegin(kind EventKind, since time.Time, now time.Time, p Probe, windowFetched int64) {
	if w.alerted {
		return
	}
	w.alerted = true
	w.alertSince = since
	if w.cfg.OnEvent == nil {
		return
	}
	w.cfg.OnEvent(Event{
		Kind:           kind,
		Since:          since,
		StalledFor:     now.Sub(since),
		Probe:          p,
		SeenAtProgress: w.seenAtProgress,
		WindowFetched:  windowFetched,
	})
}

// emitRecovered fires the recovery event for an alerted episode
// and closes it. A crawl episode still in progress (crawlStalled)
// is not recovered by a trickle of progress — its detector clears
// it separately — so callers from the progress path pass the
// current crawl state and this stays quiet while it holds.
func (w *Watchdog) emitRecovered(now time.Time, p Probe) {
	if !w.alerted {
		return
	}
	w.alerted = false
	if w.cfg.OnEvent == nil {
		return
	}
	w.cfg.OnEvent(Event{
		Kind:       EventRecovered,
		Since:      w.alertSince,
		StalledFor: now.Sub(w.alertSince),
		Probe:      p,
	})
}

// beforeExit runs the alerting layer's synchronous flush hook for
// an escalation exit, immediately before Exit itself. since is the
// episode's start (lastProgressAt for a zero-progress stall,
// crawlSince for a crawl).
func (w *Watchdog) beforeExit(since time.Time, now time.Time, p Probe) {
	if w.cfg.BeforeExit == nil {
		return
	}
	w.cfg.BeforeExit(Event{
		Kind:           EventExiting,
		Since:          since,
		StalledFor:     now.Sub(since),
		Probe:          p,
		SeenAtProgress: w.seenAtProgress,
	})
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
		// A crawl episode is the exception: its ingest trickles by
		// definition, so only its own detector may close it (see
		// evalCrawl); every other alerted episode recovers here.
		if !w.crawlStalled {
			w.emitRecovered(now, p)
		}
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
			w.emitBegin(EventStalled, w.lastProgressAt, now, p, 0)
		}
		if stallFor >= w.cfg.ExitAfter && !w.exited {
			w.exited = true
			w.logger.Printf("watchdog: ERROR ingest still stalled after %s; dumping goroutines and exiting for systemd to restart the process",
				stallFor.Round(time.Second))
			w.logger.Printf("watchdog: goroutine dump:\n%s", w.cfg.Stack())
			w.beforeExit(w.lastProgressAt, now, p)
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
		w.emitBegin(EventFrozen, w.lastProgressAt, now, p, 0)
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
		// A healthy (or frozen, or drained) window closes a crawl
		// episode: if it was alerted, this is its recovery — the
		// progress path in Tick cannot close it, since a crawl's
		// trickle counts as progress there by design.
		if w.crawlStalled && w.alerted {
			w.emitRecovered(now, p)
		}
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
		w.emitBegin(EventCrawling, w.crawlSince, now, p, fetchedDelta)
		return
	}
	if !w.exited {
		w.exited = true
		w.logger.Printf("watchdog: ERROR ingest still crawling after another %s (+%d records stored, queue_depth %d); dumping goroutines and exiting for systemd to restart the process",
			crawlWindow, fetchedDelta, p.QueueDepth)
		w.logger.Printf("watchdog: goroutine dump:\n%s", w.cfg.Stack())
		w.beforeExit(w.crawlSince, now, p)
		w.cfg.Exit(1)
	}
}
