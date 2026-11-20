package store

import (
	"context"
	"testing"
	"time"
)

func TestFetchBackoffMath(t *testing.T) {
	cases := []struct {
		fails int64
		max   time.Duration
		want  time.Duration
	}{
		{0, 24 * time.Hour, time.Hour}, // no failures yet: first interval
		{1, 24 * time.Hour, time.Hour},
		{2, 24 * time.Hour, 2 * time.Hour},
		{3, 24 * time.Hour, 4 * time.Hour},
		{4, 24 * time.Hour, 8 * time.Hour},
		{5, 24 * time.Hour, 16 * time.Hour},
		{6, 24 * time.Hour, 24 * time.Hour},  // 32h capped
		{20, 24 * time.Hour, 24 * time.Hour}, // deep counts stay capped, no overflow
		{3, 2 * time.Hour, 2 * time.Hour},    // operator cap wins over doubling
		{2, 0, 2 * time.Hour},                // max <= 0 selects the 24h default cap
		{9, 0, 24 * time.Hour},
	}
	for _, c := range cases {
		if got := FetchBackoff(c.fails, c.max); got != c.want {
			t.Errorf("FetchBackoff(%d, %v) = %v, want %v", c.fails, c.max, got, c.want)
		}
	}
}

func TestRecordFetchFailureAndClear(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Unix(1_700_000_000, 0)

	// No row: admission sees neither stored nor backoff.
	stored, next, err := s.FetchAdmission(ctx, hash)
	if err != nil || stored || next != 0 {
		t.Fatalf("fresh hash: stored=%v next=%d err=%v", stored, next, err)
	}

	// First failure: fails=1, retry in 1h.
	nextRetry, err := s.RecordFetchFailure(ctx, hash, "timeout", now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(time.Hour).Unix(); nextRetry != want {
		t.Fatalf("first failure nextRetry = %d, want %d", nextRetry, want)
	}
	att, found, err := s.FetchAttemptRow(ctx, hash)
	if err != nil || !found {
		t.Fatalf("row after first failure: found=%v err=%v", found, err)
	}
	if att.Fails != 1 || att.LastOutcome != "timeout" || att.NextRetryAt != nextRetry || att.UpdatedAt != now.Unix() {
		t.Fatalf("row after first failure: %+v", att)
	}
	if _, next, err := s.FetchAdmission(ctx, hash); err != nil || next != nextRetry {
		t.Fatalf("admission next = %d (err %v), want %d", next, err, nextRetry)
	}

	// Second failure an hour later: fails=2, backoff doubles from the
	// new now.
	now2 := now.Add(time.Hour)
	nextRetry, err = s.RecordFetchFailure(ctx, hash, "prescreen", now2, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if want := now2.Add(2 * time.Hour).Unix(); nextRetry != want {
		t.Fatalf("second failure nextRetry = %d, want %d", nextRetry, want)
	}
	att, _, _ = s.FetchAttemptRow(ctx, hash)
	if att.Fails != 2 || att.LastOutcome != "prescreen" {
		t.Fatalf("row after second failure: %+v", att)
	}

	// Success clears the row entirely.
	if err := s.ClearFetchAttempt(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.FetchAttemptRow(ctx, hash); err != nil || found {
		t.Fatalf("row after clear: found=%v err=%v", found, err)
	}
	if _, next, err := s.FetchAdmission(ctx, hash); err != nil || next != 0 {
		t.Fatalf("admission after clear: next=%d err=%v", next, err)
	}

	// A failure after a clear starts the ladder over at 1h.
	if _, err := s.RecordFetchFailure(ctx, hash, "failed", now, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	att, _, _ = s.FetchAttemptRow(ctx, hash)
	if att.Fails != 1 {
		t.Fatalf("fails after clear+failure = %d, want 1", att.Fails)
	}
}

func TestFetchAdmissionStored(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	const hash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := s.Upsert(Torrent{InfoHash: hash, Name: "x", TotalSize: 1, CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	stored, next, err := s.FetchAdmission(ctx, hash)
	if err != nil || !stored || next != 0 {
		t.Fatalf("stored hash: stored=%v next=%d err=%v", stored, next, err)
	}
	// A stored hash keeps reporting stored even with a backoff row
	// (the gate checks stored first; the row is leftover state).
	if _, err := s.RecordFetchFailure(ctx, hash, "timeout", time.Unix(100, 0), time.Hour); err != nil {
		t.Fatal(err)
	}
	stored, next, err = s.FetchAdmission(ctx, hash)
	if err != nil || !stored || next == 0 {
		t.Fatalf("stored+backoff hash: stored=%v next=%d err=%v", stored, next, err)
	}
}

func TestPruneFetchAttempts(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	// Two rows whose backoff expired long ago, one still backing off.
	for _, h := range []string{"1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"} {
		if _, err := s.RecordFetchFailure(ctx, h, "timeout", now.Add(-30*24*time.Hour), time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RecordFetchFailure(ctx, "3333333333333333333333333333333333333333", "timeout", now, time.Hour); err != nil {
		t.Fatal(err)
	}
	// Cutoff: backoff expired more than 7 days before now.
	n, err := s.PruneFetchAttempts(ctx, now.Add(-7*24*time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned %d rows, want 2", n)
	}
	if _, found, _ := s.FetchAttemptRow(ctx, "3333333333333333333333333333333333333333"); !found {
		t.Fatal("live backoff row was pruned")
	}
	// Second sweep is a no-op.
	if n, err := s.PruneFetchAttempts(ctx, now.Add(-7*24*time.Hour).Unix()); err != nil || n != 0 {
		t.Fatalf("second prune: n=%d err=%v", n, err)
	}
}
