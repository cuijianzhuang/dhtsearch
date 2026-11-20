package admission

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"
	"time"

	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/store"
)

var testNow = time.Unix(1_700_000_000, 0)

const testHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type failureRecord struct {
	hash, outcome string
	now           time.Time
	maxBackoff    time.Duration
}

// fakeStore is an in-memory admission.Store.
type fakeStore struct {
	stored  map[string]bool
	backoff map[string]int64
	err     error

	lookups  int
	cleared  []string
	failures []failureRecord
}

func newFakeStore() *fakeStore {
	return &fakeStore{stored: map[string]bool{}, backoff: map[string]int64{}}
}

func (f *fakeStore) FetchAdmission(_ context.Context, hash string) (bool, int64, error) {
	f.lookups++
	if f.err != nil {
		return false, 0, f.err
	}
	return f.stored[hash], f.backoff[hash], nil
}

func (f *fakeStore) RecordFetchFailure(_ context.Context, hash, outcome string, now time.Time, maxBackoff time.Duration) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.failures = append(f.failures, failureRecord{hash, outcome, now, maxBackoff})
	return now.Add(time.Hour).Unix(), nil
}

func (f *fakeStore) ClearFetchAttempt(_ context.Context, hash string) error {
	if f.err != nil {
		return f.err
	}
	f.cleared = append(f.cleared, hash)
	return nil
}

func newGate(st Store) *Gate {
	g := New(st, log.New(io.Discard, "", 0))
	g.NowFunc = func() time.Time { return testNow }
	return g
}

func TestAdmitStoredAlwaysSkipped(t *testing.T) {
	st := newFakeStore()
	st.stored[testHash] = true
	g := newGate(st)
	items := []metadata.Item{
		{Hash: testHash, Seeders: -1, Source: metadata.SourceDHT},
		{Hash: testHash, Seeders: 42, Source: metadata.SourceScrape},
		{Hash: testHash, Seeders: -1, Source: metadata.SourceFeed},
	}
	for _, it := range items {
		if g.Admit(it) {
			t.Errorf("stored hash admitted for source %v", it.Source)
		}
	}
	if stored, backoff := g.Skipped(); stored != 3 || backoff != 0 {
		t.Fatalf("skipped = (%d, %d), want (3, 0)", stored, backoff)
	}
}

func TestAdmitBackoffBySource(t *testing.T) {
	st := newFakeStore()
	st.backoff[testHash] = testNow.Add(time.Hour).Unix() // backoff active
	g := newGate(st)
	cases := []struct {
		name string
		item metadata.Item
		want bool
	}{
		{"dht refused", metadata.Item{Hash: testHash, Seeders: -1, Source: metadata.SourceDHT}, false},
		{"scrape with seeders bypasses", metadata.Item{Hash: testHash, Seeders: 5, Source: metadata.SourceScrape}, true},
		{"scrape zero seeders refused", metadata.Item{Hash: testHash, Seeders: 0, Source: metadata.SourceScrape}, false},
		{"scrape unknown seeders refused", metadata.Item{Hash: testHash, Seeders: -1, Source: metadata.SourceScrape}, false},
		{"feed bypasses", metadata.Item{Hash: testHash, Seeders: -1, Source: metadata.SourceFeed}, true},
	}
	for _, c := range cases {
		if got := g.Admit(c.item); got != c.want {
			t.Errorf("%s: Admit = %v, want %v", c.name, got, c.want)
		}
	}
	if stored, backoff := g.Skipped(); stored != 0 || backoff != 3 {
		t.Fatalf("skipped = (%d, %d), want (0, 3)", stored, backoff)
	}
}

func TestAdmitBackoffExpired(t *testing.T) {
	st := newFakeStore()
	st.backoff[testHash] = testNow.Add(-time.Second).Unix()
	g := newGate(st)
	if !g.Admit(metadata.Item{Hash: testHash, Seeders: -1, Source: metadata.SourceDHT}) {
		t.Fatal("expired backoff refused the hash")
	}
	if _, backoff := g.Skipped(); backoff != 0 {
		t.Fatalf("backoff skips = %d, want 0", backoff)
	}
}

func TestAdmitDisabledAndError(t *testing.T) {
	st := newFakeStore()
	st.stored[testHash] = true
	g := newGate(st)
	g.EnabledFunc = func() bool { return false }
	if !g.Admit(metadata.Item{Hash: testHash, Source: metadata.SourceDHT}) {
		t.Fatal("disabled gate refused an item")
	}
	if st.lookups != 0 {
		t.Fatalf("disabled gate consulted the store %d times", st.lookups)
	}

	st2 := newFakeStore()
	st2.err = errors.New("db down")
	g2 := newGate(st2)
	if !g2.Admit(metadata.Item{Hash: testHash, Source: metadata.SourceDHT}) {
		t.Fatal("store error must fail open (admit)")
	}
}

func TestRecordOutcome(t *testing.T) {
	st := newFakeStore()
	g := newGate(st)
	g.MaxBackoffFunc = func() time.Duration { return 48 * time.Hour }

	g.RecordOutcome(testHash, metadata.OutcomeFetched)
	if len(st.cleared) != 1 || st.cleared[0] != testHash {
		t.Fatalf("cleared = %v", st.cleared)
	}
	g.RecordOutcome(testHash, metadata.OutcomeTimedOut)
	g.RecordOutcome(testHash, metadata.OutcomePrescreened)
	g.RecordOutcome(testHash, metadata.OutcomeFailed)
	want := []string{"timeout", "prescreen", "failed"}
	if len(st.failures) != len(want) {
		t.Fatalf("failures = %v", st.failures)
	}
	for i, w := range want {
		f := st.failures[i]
		if f.outcome != w || f.hash != testHash || !f.now.Equal(testNow) || f.maxBackoff != 48*time.Hour {
			t.Errorf("failure %d = %+v, want outcome %q with gate clock and cap", i, f, w)
		}
	}

	// Disabled: nothing is recorded.
	st2 := newFakeStore()
	g2 := newGate(st2)
	g2.EnabledFunc = func() bool { return false }
	g2.RecordOutcome(testHash, metadata.OutcomeFetched)
	g2.RecordOutcome(testHash, metadata.OutcomeTimedOut)
	if len(st2.cleared) != 0 || len(st2.failures) != 0 {
		t.Fatalf("disabled gate recorded: cleared=%v failures=%v", st2.cleared, st2.failures)
	}
}

// TestFullCycle runs the whole negative-cache loop against a real
// store: admit → fail → backoff refusal → expiry → admit → success
// clears → stored refusal.
func TestFullCycle(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	now := testNow
	g := New(st, log.New(io.Discard, "", 0))
	g.NowFunc = func() time.Time { return now }
	// The gate reads the real clock through the store's admission
	// comparison only via next_retry_at, so drive expiry by moving
	// the gate clock AND recording failures in the past: record the
	// first failure at testNow, then jump the gate clock forward.
	dht := metadata.Item{Hash: testHash, Seeders: -1, Source: metadata.SourceDHT}

	if !g.Admit(dht) {
		t.Fatal("unknown hash refused")
	}
	// Simulate the fetch failing at testNow (the store records with
	// its own now parameter, which RecordOutcome takes from the
	// gate clock).
	g.RecordOutcome(testHash, metadata.OutcomeTimedOut)
	att, found, err := st.FetchAttemptRow(ctx, testHash)
	if err != nil || !found || att.Fails != 1 {
		t.Fatalf("after failure: %+v found=%v err=%v", att, found, err)
	}
	if att.NextRetryAt != testNow.Add(time.Hour).Unix() {
		t.Fatalf("next retry = %d, want %d", att.NextRetryAt, testNow.Add(time.Hour).Unix())
	}

	if g.Admit(dht) {
		t.Fatal("hash in backoff admitted")
	}
	if _, backoff := g.Skipped(); backoff != 1 {
		t.Fatalf("backoff skips = %d, want 1", backoff)
	}

	// Still inside the window at +59m; past it at +61m. The gate
	// compares the stored next_retry_at (wall clock) against its
	// own clock, so move the gate clock.
	now = testNow.Add(59 * time.Minute)
	if g.Admit(dht) {
		t.Fatal("hash admitted before backoff expiry")
	}
	now = testNow.Add(61 * time.Minute)
	if !g.Admit(dht) {
		t.Fatal("hash refused after backoff expiry")
	}

	// Success clears the row; the hash is a clean slate again.
	g.RecordOutcome(testHash, metadata.OutcomeFetched)
	if _, found, err := st.FetchAttemptRow(ctx, testHash); err != nil || found {
		t.Fatalf("after success: found=%v err=%v", found, err)
	}
	if !g.Admit(dht) {
		t.Fatal("cleared hash refused")
	}

	// Once the torrent is actually indexed, every source is refused
	// by the stored check — including a feed item, whose backoff
	// exemption does not extend to it.
	if err := st.Upsert(store.Torrent{InfoHash: testHash, Name: "x", TotalSize: 1, CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if g.Admit(dht) {
		t.Fatal("stored hash admitted (dht)")
	}
	if g.Admit(metadata.Item{Hash: testHash, Seeders: -1, Source: metadata.SourceFeed}) {
		t.Fatal("stored hash admitted (feed)")
	}
	if stored, _ := g.Skipped(); stored != 2 {
		t.Fatalf("stored skips = %d, want 2", stored)
	}
}
