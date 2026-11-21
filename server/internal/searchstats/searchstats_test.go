package searchstats

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"dhtsearch/server/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fixedNow pins the service clock at the given instant.
func fixedNow(svc *Service, ts time.Time) {
	svc.now = func() time.Time { return ts }
}

var testInstant = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

func TestRecordFlushAndWindows(t *testing.T) {
	st := openStore(t)
	svc := New(st, nil)
	fixedNow(svc, testInstant)
	ctx := context.Background()

	// Raw queries normalize before they reach the service, exactly
	// as the API hook does it.
	for _, raw := range []string{"Ubuntu", "UBUNTU", "  ubuntu ", "Dune", "Ubuntu"} {
		term, display, ok := store.NormalizeSearchTerm(raw)
		if !ok {
			t.Fatalf("NormalizeSearchTerm(%q) not ok", raw)
		}
		svc.Record(term, display)
	}
	if err := svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	day := store.SearchTermDay(testInstant)
	got, err := st.TopSearchTerms(ctx, day, 10)
	if err != nil {
		t.Fatal(err)
	}
	// Four ubuntu searches merged; the display is the LAST casing
	// recorded ("Ubuntu" from the final search).
	want := []store.SearchTermStat{
		{Term: "Ubuntu", Count: 4},
		{Term: "Dune", Count: 1},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("top = %+v, want %+v", got, want)
	}

	// A flush with nothing pending is a no-op; a second batch
	// accumulates onto the same day bucket.
	if err := svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	svc.Record("dune", "DUNE")
	if err := svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = st.TopSearchTerms(ctx, day, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != (store.SearchTermStat{Term: "DUNE", Count: 2}) {
		t.Fatalf("top after second flush = %+v", got)
	}
}

func TestRecordStampsTheSearchDay(t *testing.T) {
	st := openStore(t)
	svc := New(st, nil)
	ctx := context.Background()

	// Recorded just before midnight UTC, flushed just after: the
	// count belongs to the day of the search, not the flush.
	fixedNow(svc, time.Date(2026, 11, 21, 23, 59, 59, 0, time.UTC))
	svc.Record("ubuntu", "Ubuntu")
	fixedNow(svc, time.Date(2026, 11, 22, 0, 0, 30, 0, time.UTC))
	if err := svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	day21 := store.SearchTermDay(time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC))
	got, err := st.TopSearchTerms(ctx, day21, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("day-21 top = %+v, want the pre-midnight search", got)
	}
	got, err = st.TopSearchTerms(ctx, day21+1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("day-22 top = %+v, want empty", got)
	}
}

func TestFlushFailureKeepsCounts(t *testing.T) {
	st := openStore(t)
	svc := New(st, nil)
	fixedNow(svc, testInstant)
	svc.Record("ubuntu", "Ubuntu")
	svc.Record("ubuntu", "Ubuntu")

	// Break the store under the service: the flush must fail and
	// keep the batch, not drop it.
	st.Close()
	if err := svc.Flush(context.Background()); err == nil {
		t.Fatal("flush against a closed store succeeded, want error")
	}

	// A fresh store on the same file receives the retained batch
	// once writes work again.
	st2, err := store.Open(filepath.Join(t.TempDir(), "test2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	svc.st = st2
	if err := svc.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	day := store.SearchTermDay(testInstant)
	got, err := st2.TopSearchTerms(context.Background(), day, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Count != 2 {
		t.Fatalf("top after recovery = %+v, want ubuntu ×2", got)
	}
}

func TestPendingCapDropsNewTermsOnly(t *testing.T) {
	st := openStore(t)
	svc := New(st, nil)
	fixedNow(svc, testInstant)

	svc.Record("kept", "Kept")
	// Fill the pending map to the cap with distinct terms.
	for i := 0; i < maxPendingTerms; i++ {
		svc.Record(termName(i), termName(i))
	}
	before := svc.Dropped()
	svc.Record("overflow", "Overflow")
	if svc.Dropped() != before+1 {
		t.Fatalf("dropped = %d, want %d after one overflow term", svc.Dropped(), before+1)
	}
	// Terms already pending keep counting even at the cap.
	svc.Record("kept", "Kept")
	if err := svc.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	day := store.SearchTermDay(testInstant)
	got, err := st.TopSearchTerms(context.Background(), day, maxPendingTerms+10)
	if err != nil {
		t.Fatal(err)
	}
	var kept *store.SearchTermStat
	for i := range got {
		if got[i].Term == "Kept" {
			kept = &got[i]
		}
		if got[i].Term == "Overflow" {
			t.Fatal("overflow term was stored despite the cap")
		}
	}
	if kept == nil || kept.Count != 2 {
		t.Fatalf("kept = %+v, want count 2", kept)
	}
}

// termName renders i as a distinct lowercase term.
func termName(i int) string {
	return "term" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26)) + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
