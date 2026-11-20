package crawler

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

// fakeQuality returns a tracker on a manual clock starting at a fixed
// time, plus the function to advance it.
func fakeQuality() (*nodeQuality, func(time.Duration)) {
	cur := time.Unix(1_700_000_000, 0)
	q := newNodeQuality()
	q.now = func() time.Time { return cur }
	return q, func(d time.Duration) { cur = cur.Add(d) }
}

func TestScoreStartsNeutralAndMovesWithOutcomes(t *testing.T) {
	q, _ := fakeQuality()
	if got := q.Score("10.0.0.1:6881"); got != 0.5 {
		t.Fatalf("unknown node score=%v, want the 0.5 prior", got)
	}
	for i := 0; i < 9; i++ {
		q.Record("10.0.0.1:6881", true, 50*time.Millisecond)
	}
	if got := q.Score("10.0.0.1:6881"); math.Abs(got-10.0/11.0) > 1e-9 {
		t.Fatalf("after 9 successes score=%v, want %v (prior 1 + 9 over prior 2 + 9)", got, 10.0/11.0)
	}
	for i := 0; i < 9; i++ {
		q.Record("10.0.0.2:6881", false, 0)
	}
	if got := q.Score("10.0.0.2:6881"); math.Abs(got-1.0/11.0) > 1e-9 {
		t.Fatalf("after 9 failures score=%v, want %v", got, 1.0/11.0)
	}
	if !(q.Score("10.0.0.1:6881") > q.Score("10.0.0.3:6881") &&
		q.Score("10.0.0.3:6881") > q.Score("10.0.0.2:6881")) {
		t.Fatal("ordering broken: proven good > unknown > proven bad must hold")
	}
}

// Decay alone must not change a score (the ratio is what we last
// knew), but it must shrink the evidence weight so fresh observations
// move an old score much more than they move a fresh one.
func TestDecayDiscountsOldEvidence(t *testing.T) {
	q, advance := fakeQuality()
	for i := 0; i < 20; i++ {
		q.Record("10.0.0.1:6881", true, 10*time.Millisecond)
	}
	before := q.Score("10.0.0.1:6881") // 21/22 ≈ 0.955

	advance(3 * time.Hour) // six half-lives
	if got := q.Score("10.0.0.1:6881"); math.Abs(got-before) > 1e-9 {
		t.Fatalf("decay alone changed the score: %v -> %v", before, got)
	}
	// One failure against six-half-life-old evidence must bite hard;
	// against fresh evidence (next test line) it barely dents.
	q.Record("10.0.0.1:6881", false, 0)
	if got := q.Score("10.0.0.1:6881"); got > 0.75 {
		t.Fatalf("stale score=%v after one failure, want <= 0.75 (old evidence should be discounted)", got)
	}

	q2, _ := fakeQuality()
	for i := 0; i < 20; i++ {
		q2.Record("10.0.0.1:6881", true, 10*time.Millisecond)
	}
	q2.Record("10.0.0.1:6881", false, 0)
	if got := q2.Score("10.0.0.1:6881"); got < 0.9 {
		t.Fatalf("fresh score=%v after one failure, want >= 0.9 (fresh evidence should dominate)", got)
	}
}

func TestBanAfterConsecutiveFailuresAndRecovery(t *testing.T) {
	q, advance := fakeQuality()
	a := "10.0.0.9:6881"
	for i := 0; i < banThreshold-1; i++ {
		q.Record(a, false, 0)
	}
	if q.Banned(a) {
		t.Fatalf("banned after %d failures, want ban at %d", banThreshold-1, banThreshold)
	}
	// A single success resets the streak.
	q.Record(a, true, 10*time.Millisecond)
	for i := 0; i < banThreshold-1; i++ {
		q.Record(a, false, 0)
	}
	if q.Banned(a) {
		t.Fatal("banned despite an intervening success resetting the streak")
	}
	q.Record(a, false, 0) // fifth consecutive
	if !q.Banned(a) {
		t.Fatal("not banned after 5 consecutive failures")
	}
	// The ban expires on its own; the node is not permanently exiled.
	advance(banCooldown + time.Minute)
	if q.Banned(a) {
		t.Fatal("ban did not expire after the cooldown")
	}
}

func TestLatencyEWMA(t *testing.T) {
	q, _ := fakeQuality()
	a := "10.0.0.5:6881"
	q.Record(a, true, 100*time.Millisecond)
	q.Record(a, true, 100*time.Millisecond)
	q.Record(a, true, 200*time.Millisecond)
	q.Record(a, true, 200*time.Millisecond) // attempts=4, proven
	top := q.Top(10, 0.5)
	if len(top) != 1 {
		t.Fatalf("Top returned %d nodes, want 1", len(top))
	}
	// EWMA: 100 -> 100 -> 125 -> 143.75
	if math.Abs(top[0].LatencyMs-143.75) > 1e-9 {
		t.Fatalf("latency EWMA=%v, want 143.75", top[0].LatencyMs)
	}
	// Failures carry no latency sample and must not move the EWMA.
	q.Record(a, false, 0)
	e := q.entries[a]
	if math.Abs(e.latencyMs-143.75) > 1e-9 {
		t.Fatalf("failure moved latency EWMA to %v", e.latencyMs)
	}
}

func TestTopFiltersAndOrders(t *testing.T) {
	q, _ := fakeQuality()
	rec := func(addr string, ok bool, n int, lat time.Duration) {
		for i := 0; i < n; i++ {
			q.Record(addr, ok, lat)
		}
	}
	rec("10.0.0.1:6881", true, 9, 100*time.Millisecond) // score 10/11
	rec("10.0.0.2:6881", true, 4, 50*time.Millisecond)  // score 5/6, less evidence
	rec("10.0.0.3:6881", true, 4, 20*time.Millisecond)  // score 5/6, faster
	rec("10.0.0.4:6881", true, 2, 10*time.Millisecond)  // unproven: attempts < 3
	rec("10.0.0.5:6881", false, 9, 0)                   // low score (and banned)
	mixed := "10.0.0.6:6881"
	rec(mixed, true, 3, 10*time.Millisecond)
	rec(mixed, false, 3, 0) // score 4/8 = 0.5 exactly

	top := q.Top(10, 0.5)
	got := make([]string, len(top))
	for i, n := range top {
		got[i] = n.Addr
	}
	want := []string{"10.0.0.1:6881", "10.0.0.3:6881", "10.0.0.2:6881", "10.0.0.6:6881"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Top=%v, want %v (score, then weight, then latency; unproven and failing nodes out)", got, want)
	}
	if top := q.Top(2, 0.5); len(top) != 2 {
		t.Fatalf("Top(2) returned %d, want the cap honoured", len(top))
	}
	if top := q.Top(10, 0.95); len(top) != 0 {
		t.Fatalf("Top with minScore 0.95 returned %d, want 0", len(top))
	}
}

// The persistence round trip: Top's snapshot seeds a fresh tracker
// (the next process start) and the scores survive, discounted by the
// downtime between the two runs.
func TestSeedTopRoundTripAcrossRestart(t *testing.T) {
	q, _ := fakeQuality()
	for i := 0; i < 9; i++ {
		q.Record("10.0.0.1:6881", true, 40*time.Millisecond)
	}
	for i := 0; i < 4; i++ {
		q.Record("10.0.0.2:6881", false, 0)
		q.Record("10.0.0.2:6881", true, 80*time.Millisecond)
	}
	snap := q.Top(512, 0.5)
	if len(snap) != 2 {
		t.Fatalf("snapshot has %d nodes, want 2", len(snap))
	}

	// Restart an hour later: seed the new tracker from the snapshot.
	q2, advance2 := fakeQuality()
	advance2(time.Hour) // align its clock with "an hour after the snapshot"
	// Seed keys decay off the snapshot's UpdatedAt (an hour old on
	// this clock), so the first score read decays the weights by two
	// half-lives.
	seeded := q2.Seed(snap)
	if len(seeded) != 2 {
		t.Fatalf("seeded %d nodes, want 2", len(seeded))
	}
	s1 := q2.Score("10.0.0.1:6881")
	if math.Abs(s1-10.0/11.0) > 1e-9 {
		t.Fatalf("restored score=%v, want %v (ratio survives the restart)", s1, 10.0/11.0)
	}
	// But the restored evidence is old: two fresh failures must move
	// it far more than they move a freshly-earned identical score.
	q2.Record("10.0.0.1:6881", false, 0)
	q2.Record("10.0.0.1:6881", false, 0)
	discounted := q2.Score("10.0.0.1:6881")

	q3, _ := fakeQuality()
	for i := 0; i < 9; i++ {
		q3.Record("10.0.0.1:6881", true, 40*time.Millisecond)
	}
	q3.Record("10.0.0.1:6881", false, 0)
	q3.Record("10.0.0.1:6881", false, 0)
	fresh := q3.Score("10.0.0.1:6881")
	if !(discounted < fresh) {
		t.Fatalf("restored score after 2 failures=%v, fresh score=%v; downtime decay must discount restored evidence", discounted, fresh)
	}
}

func TestSummaryCounts(t *testing.T) {
	q, _ := fakeQuality()
	q.Touch("10.0.0.1:6881") // known, unobserved
	for i := 0; i < 3; i++ {
		q.Record("10.0.0.2:6881", true, 10*time.Millisecond)
	}
	for i := 0; i < banThreshold; i++ {
		q.Record("10.0.0.3:6881", false, 0)
	}
	sum := q.Summary()
	if sum.Tracked != 3 {
		t.Fatalf("Tracked=%d, want 3", sum.Tracked)
	}
	if sum.Banned != 1 {
		t.Fatalf("Banned=%d, want 1", sum.Banned)
	}
	// Avg over observed nodes only: (4/5 + 1/7) / 2 ≈ 0.471
	want := (4.0/5.0 + 1.0/7.0) / 2
	if math.Abs(sum.AvgScore-want) > 1e-9 {
		t.Fatalf("AvgScore=%v, want %v", sum.AvgScore, want)
	}
}

// The tracker is fed from many goroutines at once (samplers, walk,
// refill); this is the -race canary plus a lost-update check on the
// attempts counter.
func TestConcurrentRecords(t *testing.T) {
	q, _ := fakeQuality()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			addr := fmt.Sprintf("10.0.%d.1:6881", g)
			for i := 0; i < 200; i++ {
				q.Record(addr, i%3 != 0, 10*time.Millisecond)
				q.Score(addr)
				q.Touch(addr)
			}
		}(g)
	}
	wg.Wait()
	sum := q.Summary()
	if sum.Tracked != 8 {
		t.Fatalf("Tracked=%d, want 8", sum.Tracked)
	}
	for g := 0; g < 8; g++ {
		addr := fmt.Sprintf("10.0.%d.1:6881", g)
		if got := q.entries[addr].attempts; got != 200 {
			t.Fatalf("%s attempts=%d, want 200 (lost update)", addr, got)
		}
	}
}
