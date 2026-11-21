package watchdog

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// harness drives a Watchdog with synthetic ticks and records its
// side effects.
type harness struct {
	wd     *Watchdog
	logs   *bytes.Buffer
	exits  []int
	stacks int
	on     bool
	events []Event
	// order records the exit-path call sequence ("beforeExit",
	// "exit") so the flush-before-exit ordering is assertable.
	order []string
}

func newHarness() *harness {
	h := &harness{logs: &bytes.Buffer{}, on: true}
	h.wd = New(Config{
		Active:  true,
		Enabled: func() bool { return h.on },
		Logger:  log.New(h.logs, "", 0),
		Exit: func(code int) {
			h.exits = append(h.exits, code)
			h.order = append(h.order, "exit")
		},
		Stack:   func() string { h.stacks++; return "FAKE STACK" },
		OnEvent: func(ev Event) { h.events = append(h.events, ev) },
		BeforeExit: func(ev Event) {
			h.events = append(h.events, ev)
			h.order = append(h.order, "beforeExit")
		},
	})
	return h
}

func (h *harness) kinds() []EventKind {
	out := make([]EventKind, 0, len(h.events))
	for _, ev := range h.events {
		out = append(out, ev.Kind)
	}
	return out
}

var t0 = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

// tickN advances n one-minute ticks, mutating the probe with fn
// (i is the tick index, starting at 0 for the initial sample).
func (h *harness) tickN(n int, fn func(i int, p *Probe)) {
	p := Probe{}
	for i := 0; i < n; i++ {
		fn(i, &p)
		h.wd.Tick(t0.Add(time.Duration(i)*time.Minute), p)
	}
}

func TestNormalGrowthNoAction(t *testing.T) {
	h := newHarness()
	h.tickN(40, func(i int, p *Probe) {
		p.Seen += 1000
		p.Fetched += 30
	})
	if len(h.exits) != 0 {
		t.Fatalf("exits = %v, want none", h.exits)
	}
	if h.stacks != 0 {
		t.Fatalf("stack dumps = %d, want 0", h.stacks)
	}
	if strings.Contains(h.logs.String(), "ERROR") {
		t.Fatalf("unexpected ERROR in log:\n%s", h.logs.String())
	}
	if st := h.wd.Status(); st.State != StateOK {
		t.Fatalf("status = %v, want ok", st.State)
	}
}

// The status line fires every 10 minutes with the window's deltas.
func TestStatusLine(t *testing.T) {
	h := newHarness()
	h.tickN(11, func(i int, p *Probe) {
		p.Seen += 100
		p.Fetched += 5
		p.TimedOut += 40
		p.Prescreen += 60
		p.QueueDepth = 7
	})
	out := h.logs.String()
	if !strings.Contains(out, "pipeline: +1000 seen +50 fetched queue_depth 7 +400 timed_out +600 prescreen_dropped in 10m0s") {
		t.Fatalf("status line missing or wrong:\n%s", out)
	}
}

func TestStallWarnsAt15ExitsAt30(t *testing.T) {
	h := newHarness()
	// One minute of progress, then discovery keeps moving while
	// ingest freezes.
	h.tickN(35, func(i int, p *Probe) {
		p.Seen += 1000
		if i == 0 {
			p.Fetched = 10
		}
	})
	// Tick i=15 is 15 minutes after the last progress (i=0): the
	// warning stack must have been dumped exactly once by then, and
	// the exit must have fired at i=30 with a second dump.
	if h.stacks != 2 {
		t.Fatalf("stack dumps = %d, want 2 (warn + exit)", h.stacks)
	}
	if len(h.exits) != 1 || h.exits[0] != 1 {
		t.Fatalf("exits = %v, want [1]", h.exits)
	}
	if st := h.wd.Status(); st.State != StateStalled {
		t.Fatalf("status = %v, want stalled", st.State)
	}
	if !strings.Contains(h.logs.String(), "ingest stalled") || !strings.Contains(h.logs.String(), "exiting for systemd") {
		t.Fatalf("log missing stall/exit lines:\n%s", h.logs.String())
	}
}

func TestStallDoesNotWarnEarly(t *testing.T) {
	h := newHarness()
	h.tickN(15, func(i int, p *Probe) {
		p.Seen += 1000
		if i == 0 {
			p.Fetched = 10
		}
	})
	// i=14 is 14 minutes after last progress: below WarnAfter.
	if h.stacks != 0 || len(h.exits) != 0 {
		t.Fatalf("acted early: stacks=%d exits=%v", h.stacks, h.exits)
	}
}

func TestStallRecoveryResetsEpisode(t *testing.T) {
	h := newHarness()
	// Stall long enough to warn (but not exit), recover, then stall
	// again: the second episode must warn again and the exit clock
	// must restart from the recovery.
	phase := func(i int, p *Probe) {
		p.Seen += 1000
		switch {
		case i == 0:
			p.Fetched = 10
		case i == 20:
			p.Fetched = 25 // recovery at minute 20
		}
	}
	h.tickN(40, phase)
	if h.stacks != 2 {
		t.Fatalf("stack dumps = %d, want 2 (one per episode)", h.stacks)
	}
	if len(h.exits) != 0 {
		t.Fatalf("exits = %v, want none (second episode only 19m old)", h.exits)
	}
	if st := h.wd.Status(); st.State != StateStalled {
		t.Fatalf("status = %v, want stalled", st.State)
	}
}

func TestBothFrozenWarnsNeverExits(t *testing.T) {
	h := newHarness()
	// Nothing moves at all for over two hours.
	h.tickN(130, func(i int, p *Probe) {})
	if len(h.exits) != 0 {
		t.Fatalf("exits = %v, want none for a both-frozen pipeline", h.exits)
	}
	// Dumps at minute 15 (first detection) and minute 75 (hourly
	// rewarn): exactly 2 in 130 ticks.
	if h.stacks != 2 {
		t.Fatalf("stack dumps = %d, want 2", h.stacks)
	}
	if st := h.wd.Status(); st.State != StateFrozen {
		t.Fatalf("status = %v, want frozen", st.State)
	}
	if !strings.Contains(h.logs.String(), "pipeline frozen") {
		t.Fatalf("log missing frozen line:\n%s", h.logs.String())
	}
}

func TestDisabledNeverActs(t *testing.T) {
	h := newHarness()
	h.on = false
	h.tickN(60, func(i int, p *Probe) {
		p.Seen += 1000 // stalled shape, but the watchdog is off
	})
	if h.stacks != 0 || len(h.exits) != 0 {
		t.Fatalf("disabled watchdog acted: stacks=%d exits=%v", h.stacks, h.exits)
	}
	if st := h.wd.Status(); st.State != StateDisabled {
		t.Fatalf("status = %v, want disabled", st.State)
	}
}

func TestInactiveReportsDisabled(t *testing.T) {
	wd := New(Config{Active: false})
	if st := wd.Status(); st.State != StateDisabled {
		t.Fatalf("status = %v, want disabled", st.State)
	}
}

// The 2026-11-20 recurrence: ingest trickles (+2/min = +20 per
// window, far under the floor) while discovery moves and the queue
// never drains. The trickle keeps the zero-progress clock reset, so
// only the crawl detector can catch it: dump at the first crawl
// window (minute 10), dump + exit at the second (minute 20).
func TestCrawlDetectedThenExits(t *testing.T) {
	h := newHarness()
	h.tickN(25, func(i int, p *Probe) {
		p.Seen += 1000
		p.Fetched += 2
		p.QueueDepth = 15000
	})
	if h.stacks != 2 {
		t.Fatalf("stack dumps = %d, want 2 (first crawl window + exit window)", h.stacks)
	}
	if len(h.exits) != 1 || h.exits[0] != 1 {
		t.Fatalf("exits = %v, want [1]", h.exits)
	}
	if st := h.wd.Status(); st.State != StateStalled {
		t.Fatalf("status = %v, want stalled", st.State)
	}
	out := h.logs.String()
	if !strings.Contains(out, "ingest crawling") || !strings.Contains(out, "still crawling") {
		t.Fatalf("log missing crawl lines:\n%s", out)
	}
}

// The legitimate post-restart plateau: workers chew a backlog of
// mostly dead hashes, so few records land — but the queue falls
// steadily. That must never be flagged as a crawl.
func TestCrawlQueueDrainingNotFlagged(t *testing.T) {
	h := newHarness()
	h.tickN(35, func(i int, p *Probe) {
		p.Seen += 1000
		p.Fetched += 2
		p.QueueDepth = 15000 - 400*(i+1)
	})
	if h.stacks != 0 || len(h.exits) != 0 {
		t.Fatalf("draining queue flagged: stacks=%d exits=%v", h.stacks, h.exits)
	}
	if strings.Contains(h.logs.String(), "crawling") {
		t.Fatalf("unexpected crawl line:\n%s", h.logs.String())
	}
	if st := h.wd.Status(); st.State != StateOK {
		t.Fatalf("status = %v, want ok", st.State)
	}
}

// A healthy saturated pipeline: the queue stays pinned because
// discovery refills it as fast as workers drain it, but stored
// records grow at full rate. Not a crawl.
func TestCrawlHealthyThroughputNotFlagged(t *testing.T) {
	h := newHarness()
	h.tickN(35, func(i int, p *Probe) {
		p.Seen += 1000
		p.Fetched += 35 // 350 per window, around the healthy baseline
		p.QueueDepth = 15000
	})
	if h.stacks != 0 || len(h.exits) != 0 {
		t.Fatalf("healthy throughput flagged: stacks=%d exits=%v", h.stacks, h.exits)
	}
	if st := h.wd.Status(); st.State != StateOK {
		t.Fatalf("status = %v, want ok", st.State)
	}
}

// Episode events: one begin per stall episode at the warn
// transition, one recovery when progress resumes, and a second
// begin when a later stall forms a new episode.
func TestStallEventsOncePerEpisode(t *testing.T) {
	h := newHarness()
	phase := func(i int, p *Probe) {
		p.Seen += 1000
		switch {
		case i == 0:
			p.Fetched = 10
		case i == 20:
			p.Fetched = 25 // recovery
		}
	}
	h.tickN(40, phase)
	want := []EventKind{EventStalled, EventRecovered, EventStalled}
	got := h.kinds()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
	stall := h.events[0]
	if stall.Probe.Seen == 0 || stall.StalledFor < 15*time.Minute {
		t.Fatalf("stall event lacks context: %+v", stall)
	}
	rec := h.events[1]
	if rec.StalledFor <= 0 || rec.Since.IsZero() {
		t.Fatalf("recovery event lacks episode age: %+v", rec)
	}
}

// A crawl episode emits its begin at the first crawl window and
// its recovery when a healthy window closes it — the trickle in
// between must not read as recovery.
func TestCrawlEvents(t *testing.T) {
	h := newHarness()
	h.tickN(32, func(i int, p *Probe) {
		p.Seen += 1000
		p.QueueDepth = 15000
		switch {
		case i <= 10:
			p.Fetched += 2 // window 1: crawl
		case i <= 20:
			p.Fetched += 40 // window 2: healthy
		default:
			p.Fetched += 40
		}
	})
	want := []EventKind{EventCrawling, EventRecovered}
	got := h.kinds()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if h.events[0].WindowFetched <= 0 || h.events[0].WindowFetched >= crawlMinFetched {
		t.Fatalf("crawl event window fetched = %d, want within the crawl band", h.events[0].WindowFetched)
	}
}

// A both-frozen pipeline emits one frozen begin per episode.
func TestFrozenEvent(t *testing.T) {
	h := newHarness()
	h.tickN(40, func(i int, p *Probe) {})
	want := []EventKind{EventFrozen}
	got := h.kinds()
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

// The exit path must run the BeforeExit flush hook immediately
// before Exit, with an exiting event describing the episode.
func TestBeforeExitRunsBeforeExit(t *testing.T) {
	h := newHarness()
	h.tickN(35, func(i int, p *Probe) {
		p.Seen += 1000
		if i == 0 {
			p.Fetched = 10
		}
	})
	if len(h.exits) != 1 {
		t.Fatalf("exits = %v, want one", h.exits)
	}
	if len(h.order) != 2 || h.order[0] != "beforeExit" || h.order[1] != "exit" {
		t.Fatalf("exit-path order = %v, want [beforeExit exit]", h.order)
	}
	last := h.events[len(h.events)-1]
	if last.Kind != EventExiting {
		t.Fatalf("last event = %v, want exiting", last.Kind)
	}
	if last.StalledFor < 30*time.Minute {
		t.Fatalf("exiting event stalledFor = %v, want >= 30m", last.StalledFor)
	}
}

// A healthy pipeline emits nothing.
func TestNoEventsWhenHealthy(t *testing.T) {
	h := newHarness()
	h.tickN(40, func(i int, p *Probe) {
		p.Seen += 1000
		p.Fetched += 30
	})
	if len(h.events) != 0 {
		t.Fatalf("events = %v, want none", h.events)
	}
}

// A crawl window followed by a healthy window resets the
// escalation: a later crawl is a new episode (dump again, but the
// exit needs its own second consecutive crawl window).
func TestCrawlRecoveryResetsEscalation(t *testing.T) {
	h := newHarness()
	h.tickN(32, func(i int, p *Probe) {
		p.Seen += 1000
		p.QueueDepth = 15000
		switch {
		case i <= 10:
			p.Fetched += 2 // window 1: crawl -> dump
		case i <= 20:
			p.Fetched += 40 // window 2: healthy -> episode resets
		default:
			p.Fetched += 2 // window 3: crawl again -> dump, no exit
		}
	})
	if h.stacks != 2 {
		t.Fatalf("stack dumps = %d, want 2 (one per crawl episode)", h.stacks)
	}
	if len(h.exits) != 0 {
		t.Fatalf("exits = %v, want none (second episode only one window old)", h.exits)
	}
	if st := h.wd.Status(); st.State != StateStalled {
		t.Fatalf("status = %v, want stalled", st.State)
	}
}
