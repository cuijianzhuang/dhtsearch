package scraper

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"
)

func conns(hosts ...string) []*trackerConn {
	out := make([]*trackerConn, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, &trackerConn{host: h})
	}
	return out
}

func hostsOf(tcs []*trackerConn) []string {
	out := make([]string, 0, len(tcs))
	for _, tc := range tcs {
		out = append(out, tc.host)
	}
	return out
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// fakeClockQuality returns a TrackerQuality on a manually advanced
// clock, plus the advance function.
func fakeClockQuality(start time.Time) (*TrackerQuality, func(time.Duration)) {
	cur := start
	q := NewTrackerQuality()
	q.now = func() time.Time { return cur }
	return q, func(d time.Duration) { cur = cur.Add(d) }
}

// TestTrackerQualityOrdersByScore: selection asks the proven,
// productive tracker first, the unknown tracker at its neutral
// prior next, and the reliable-but-barren tracker last — its
// answers are real but it never finds a seeder, so it yields
// priority without losing its seat.
func TestTrackerQualityOrdersByScore(t *testing.T) {
	q := NewTrackerQuality()
	for i := 0; i < 10; i++ {
		q.record("rich:1", true, 20*time.Millisecond, 10, 10)
		q.record("barren:1", true, 20*time.Millisecond, 10, 0)
	}
	if got := q.Score("rich:1"); got < 0.9 {
		t.Fatalf("rich score = %v, want ~0.92", got)
	}
	if got := q.Score("unknown:1"); got != 0.5 {
		t.Fatalf("unknown score = %v, want the 0.5 prior", got)
	}
	if got := q.Score("barren:1"); got < 0.4 || got > 0.5 {
		t.Fatalf("barren score = %v, want ~0.46 (reliable, zero yield)", got)
	}
	got := hostsOf(q.selectTrackers(conns("barren:1", "unknown:1", "rich:1")))
	want := []string{"rich:1", "unknown:1", "barren:1"}
	if len(got) != len(want) {
		t.Fatalf("selection = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("selection = %v, want %v", got, want)
		}
	}
}

// TestTrackerQualitySkipsDeadButExplores: trackers with conclusive
// failure evidence sit out rounds — except the exploration quota,
// which re-probes the sat-out pool least-recently-queried first so
// the pool rotates and no tracker is exiled forever.
func TestTrackerQualitySkipsDeadButExplores(t *testing.T) {
	q, advance := fakeClockQuality(time.Unix(1_700_000_000, 0))
	for i := 0; i < 25; i++ {
		q.record("good:1", true, 10*time.Millisecond, 5, 5)
	}
	dead := []string{"d1:1", "d2:1", "d3:1"}
	for _, d := range dead {
		for i := 0; i < 25; i++ {
			advance(time.Second)
			q.record(d, false, 0, 0, 0)
		}
		advance(time.Minute) // stagger the verdicts: d1 oldest, d3 newest
	}
	all := conns("good:1", "d1:1", "d2:1", "d3:1")

	// Round 1: the good tracker plus the two oldest-verdict dead
	// ones as explorers; d3 sits out entirely.
	got := hostsOf(q.selectTrackers(all))
	if len(got) != 3 || !containsStr(got, "good:1") || !containsStr(got, "d1:1") || !containsStr(got, "d2:1") {
		t.Fatalf("round 1 selection = %v, want good+d1+d2 (d3 sat out)", got)
	}
	// The explorers are queried and fail again, refreshing their
	// verdicts — so round 2 explores d3 and one of d1/d2, and the
	// other sits out: the pool rotated.
	advance(time.Minute)
	q.record("d1:1", false, 0, 0, 0)
	q.record("d2:1", false, 0, 0, 0)
	got = hostsOf(q.selectTrackers(all))
	if len(got) != 3 || !containsStr(got, "good:1") || !containsStr(got, "d3:1") {
		t.Fatalf("round 2 selection = %v, want good+d3+one of d1/d2", got)
	}
	if containsStr(got, "d1:1") && containsStr(got, "d2:1") {
		t.Fatalf("round 2 selection = %v, want one of d1/d2 sat out (rotation)", got)
	}
}

// TestTrackerQualityKeepsReliableBarren: a tracker that always
// answers — even with all-zero answers, the evidence dead-torrent
// detection runs on — is never sat out, however many rounds it
// goes without reporting a seeder.
func TestTrackerQualityKeepsReliableBarren(t *testing.T) {
	q := NewTrackerQuality()
	for i := 0; i < 60; i++ {
		q.record("barren:1", true, 5*time.Millisecond, 10, 0)
		q.record("rich:1", true, 5*time.Millisecond, 10, 10)
	}
	got := hostsOf(q.selectTrackers(conns("barren:1", "rich:1")))
	if len(got) != 2 {
		t.Fatalf("selection = %v, want both trackers asked", got)
	}
}

// TestTrackerQualityNeverSelectsNobody: when every tracker is
// sat out, selection still asks somebody (the exploration quota),
// so a network-wide outage cannot freeze the grading at its last
// verdict — rounds keep producing fresh evidence.
func TestTrackerQualityNeverSelectsNobody(t *testing.T) {
	q := NewTrackerQuality()
	for _, d := range []string{"d1:1", "d2:1", "d3:1", "d4:1", "d5:1"} {
		for i := 0; i < 25; i++ {
			q.record(d, false, 0, 0, 0)
		}
	}
	got := q.selectTrackers(conns("d1:1", "d2:1", "d3:1", "d4:1", "d5:1"))
	if len(got) != explorePerRound {
		t.Fatalf("selection = %d trackers, want the %d explorers", len(got), explorePerRound)
	}
}

// TestTrackerQualityDecay: decay alone never moves a score (the
// ratio is preserved), but it shrinks the old evidence's weight —
// after a long idle, a single failure bites the way it would on a
// fresh tracker, instead of bouncing off a mountain of ancient
// successes.
func TestTrackerQualityDecay(t *testing.T) {
	q, advance := fakeClockQuality(time.Unix(1_700_000_000, 0))
	for i := 0; i < 40; i++ {
		q.record("t:1", true, 10*time.Millisecond, 10, 10)
	}
	before := q.Score("t:1")
	if before < 0.95 {
		t.Fatalf("score before idle = %v, want ~0.98", before)
	}
	advance(10 * trackerHalfLife)
	if after := q.Score("t:1"); math.Abs(after-before) > 0.01 {
		t.Fatalf("score after idle = %v, want unchanged %v (decay preserves the ratio)", after, before)
	}
	q.record("t:1", false, 0, 0, 0)
	if got := q.Score("t:1"); got > 0.8 {
		t.Fatalf("score after one post-idle failure = %v, want a hard bite (< 0.8)", got)
	}
}

// TestTrackerQualitySnapshotSeedRoundTrip: proven trackers'
// snapshots restore into a fresh tracker with their scores (and
// their verdicts) intact; unproven trackers are not snapshotted.
func TestTrackerQualitySnapshotSeedRoundTrip(t *testing.T) {
	q, advance := fakeClockQuality(time.Unix(1_700_000_000, 0))
	for i := 0; i < 10; i++ {
		q.record("rich:1", true, 15*time.Millisecond, 10, 8)
	}
	for i := 0; i < 25; i++ {
		advance(time.Second)
		q.record("dead:1", false, 0, 0, 0)
	}
	q.record("new:1", true, 15*time.Millisecond, 10, 10) // 1 attempt: unproven

	snap := q.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot = %v, want rich+dead only (new is unproven)", snap)
	}
	q2, _ := fakeClockQuality(time.Unix(1_700_000_000, 0).Add(30 * time.Second))
	q2.Seed(snap)
	if got, want := q2.Score("rich:1"), q.Score("rich:1"); math.Abs(got-want) > 0.02 {
		t.Fatalf("restored rich score = %v, want ~%v", got, want)
	}
	// The dead tracker's verdict survives the restart: pooled with
	// enough other trackers, it is still beyond the exploration
	// quota in some rounds (it competes for 2 explorer slots).
	got := hostsOf(q2.selectTrackers(conns("rich:1", "dead:1", "u1:1", "u2:1", "u3:1")))
	if !containsStr(got, "rich:1") {
		t.Fatalf("selection = %v, want rich asked", got)
	}
	if s := q2.Score("dead:1"); s > 0.1 {
		t.Fatalf("restored dead score = %v, want its basement verdict intact", s)
	}
}

// TestRescraperSelectionSkipsSatOutTrackers: with a shared quality
// tracker, a re-scrape round asks the healthy tracker and only the
// exploration quota of the sat-out dead ones; the healthy
// tracker's all-zero answer still covers the hash (present with
// zeros) — selection changes who is asked, never what an answer
// means.
func TestRescraperSelectionSkipsSatOutTrackers(t *testing.T) {
	q, advance := fakeClockQuality(time.Unix(1_700_000_000, 0))
	dead := []string{"d1.invalid:1111", "d2.invalid:2222", "d3.invalid:3333"}
	for _, d := range dead {
		for i := 0; i < 25; i++ {
			advance(time.Second)
			q.record(d, false, 0, 0, 0)
		}
		advance(time.Minute)
	}
	advance(time.Minute)

	r, err := NewRescraper(Config{
		Trackers: []string{
			"udp://good.invalid:9999/announce",
			"udp://d1.invalid:1111/announce",
			"udp://d2.invalid:2222/announce",
			"udp://d3.invalid:3333/announce",
		},
		Quality: q,
		Logger:  quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var mu sync.Mutex
	calls := map[string]int{}
	r.scrapeTracker = func(_ context.Context, tc *trackerConn, ihs [][20]byte) ([]SwarmSample, error) {
		mu.Lock()
		calls[tc.host]++
		mu.Unlock()
		return make([]SwarmSample, len(ihs)), nil // answers, all-zero
	}

	h := hashN(7)
	got := r.Scrape(context.Background(), []string{h})
	if len(got) != 1 || got[0].Hash != h || got[0].Seeders != 0 {
		t.Fatalf("round = %v, want the hash present with zeros (covered by the healthy tracker)", got)
	}
	if calls["good.invalid:9999"] != 1 {
		t.Fatalf("good tracker calls = %d, want 1", calls["good.invalid:9999"])
	}
	deadCalls := calls["d1.invalid:1111"] + calls["d2.invalid:2222"] + calls["d3.invalid:3333"]
	if deadCalls != explorePerRound {
		t.Fatalf("dead tracker calls = %d (%v), want exactly the %d explorers", deadCalls, calls, explorePerRound)
	}
}
