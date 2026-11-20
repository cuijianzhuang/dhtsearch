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
}

func newHarness() *harness {
	h := &harness{logs: &bytes.Buffer{}, on: true}
	h.wd = New(Config{
		Active:  true,
		Enabled: func() bool { return h.on },
		Logger:  log.New(h.logs, "", 0),
		Exit:    func(code int) { h.exits = append(h.exits, code) },
		Stack:   func() string { h.stacks++; return "FAKE STACK" },
	})
	return h
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
