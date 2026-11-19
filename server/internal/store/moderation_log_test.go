package store

import (
	"context"
	"fmt"
	"testing"
)

func TestModerationLogRoundTripAndPrune(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	if err := s.LogModeration(nil, 100); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
	recs := []ModerationRecord{
		{InfoHash: "h1", Name: "Clean Movie", Label: "ok", Action: "kept"},
		{InfoHash: "h2", Name: "Bad Movie", Label: "adult", Action: "deleted"},
		{InfoHash: "h3", Name: "Spammy", Label: "spam", Action: "would_delete"},
	}
	if err := s.LogModeration(recs, 1234); err != nil {
		t.Fatal(err)
	}
	got, err := s.RecentModeration(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("recent = %d rows, want 3", len(got))
	}
	// Newest first: the last record of the batch leads.
	if got[0].InfoHash != "h3" || got[0].Label != "spam" || got[0].Action != "would_delete" {
		t.Fatalf("newest record = %+v", got[0])
	}
	if got[2].InfoHash != "h1" || got[2].CreatedAt != 1234 {
		t.Fatalf("oldest record = %+v", got[2])
	}

	// Pruning keeps exactly moderationLogKeep newest rows.
	bulk := make([]ModerationRecord, 0, moderationLogKeep)
	for i := 0; i < moderationLogKeep; i++ {
		bulk = append(bulk, ModerationRecord{
			InfoHash: fmt.Sprintf("bulk-%d", i), Name: "Bulk", Label: "ok", Action: "kept",
		})
	}
	if err := s.LogModeration(bulk, 2000); err != nil {
		t.Fatal(err)
	}
	got, err = s.RecentModeration(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 200 {
		t.Fatalf("recent page = %d rows, want the 200 cap", len(got))
	}
	var total int
	if err := s.queryRow(`SELECT COUNT(*) FROM moderation_log`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != moderationLogKeep {
		t.Fatalf("log holds %d rows, want pruned to %d", total, moderationLogKeep)
	}
	if got[0].InfoHash != fmt.Sprintf("bulk-%d", moderationLogKeep-1) {
		t.Fatalf("newest after prune = %q", got[0].InfoHash)
	}
}
