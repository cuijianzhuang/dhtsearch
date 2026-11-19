package store

import (
	"testing"

	"dhtsearch/server/internal/filter"
)

func seedAdminStore(t *testing.T) *Store {
	t.Helper()
	s := openTest(t)
	for _, r := range []Torrent{
		{InfoHash: "aa", Name: "Alpha Movie 2024", TotalSize: 1 << 30, FileCount: 1, CreatedAt: 100,
			Files: []filter.File{{Path: "alpha.mkv", Size: 1 << 30}}},
		{InfoHash: "bb", Name: "Beta Series S01", TotalSize: 2 << 30, FileCount: 2, CreatedAt: 200},
		{InfoHash: "cc", Name: "Alpha Documentary", TotalSize: 3 << 30, FileCount: 1, CreatedAt: 300},
	} {
		if err := s.Upsert(r); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestAdminListFilters(t *testing.T) {
	s := seedAdminStore(t)
	ctx := t.Context()

	items, total, err := s.AdminList(ctx, "", AdminStatusAll, 1, 2)
	if err != nil || total != 3 || len(items) != 2 || items[0].InfoHash != "cc" {
		t.Fatalf("list all: items=%v total=%d err=%v", items, total, err)
	}
	if items[0].Files != nil {
		t.Fatalf("list must not load files: %+v", items[0])
	}

	// Keyword filter shares Search semantics.
	_, total, err = s.AdminList(ctx, "alpha", AdminStatusAll, 1, 10)
	if err != nil || total != 2 {
		t.Fatalf("keyword: total=%d err=%v", total, err)
	}

	// Review-status filter.
	if n, err := s.MarkReviewed([]string{"aa"}, 500); err != nil || n != 1 {
		t.Fatalf("mark reviewed: n=%d err=%v", n, err)
	}
	items, total, err = s.AdminList(ctx, "", AdminStatusUnreviewed, 1, 10)
	if err != nil || total != 2 {
		t.Fatalf("unreviewed: total=%d err=%v", total, err)
	}
	for _, it := range items {
		if it.ReviewedAt != 0 {
			t.Fatalf("unreviewed list contains reviewed row: %+v", it)
		}
	}
	_, total, err = s.AdminList(ctx, "", AdminStatusReviewed, 1, 10)
	if err != nil || total != 1 {
		t.Fatalf("reviewed: total=%d err=%v", total, err)
	}
}

func TestAdminGetDeleteUnblock(t *testing.T) {
	s := seedAdminStore(t)
	ctx := t.Context()

	got, err := s.AdminGet(ctx, "aa")
	if err != nil || got == nil {
		t.Fatalf("get: %v err=%v", got, err)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "alpha.mkv" || got.RawName != "Alpha Movie 2024" {
		t.Fatalf("get payload: %+v", got)
	}
	missing, err := s.AdminGet(ctx, "zz")
	if err != nil || missing != nil {
		t.Fatalf("get missing: %v err=%v", missing, err)
	}

	// Delete removes without blocking: the hash can be indexed again.
	deleted, err := s.AdminDelete(ctx, "aa")
	if err != nil || !deleted {
		t.Fatalf("delete: %v err=%v", deleted, err)
	}
	if blocked, _ := s.IsBlocked("aa"); blocked {
		t.Fatal("delete must not block")
	}
	if err := s.Upsert(Torrent{InfoHash: "aa", Name: "Alpha Movie 2024", CreatedAt: 400}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Count(ctx); n != 3 {
		t.Fatalf("reinsert after delete: count=%d", n)
	}
	again, err := s.AdminDelete(ctx, "zz")
	if err != nil || again {
		t.Fatalf("delete missing: %v err=%v", again, err)
	}

	// Block then unblock.
	if _, err := s.Block([]string{"bb"}, []string{"Beta Series S01"}, "admin", 600); err != nil {
		t.Fatal(err)
	}
	entries, total, err := s.AdminBlockedList(ctx, 1, 10)
	if err != nil || total != 1 || entries[0].InfoHash != "bb" || entries[0].Reason != "admin" {
		t.Fatalf("blocked list: %v total=%d err=%v", entries, total, err)
	}
	unblocked, err := s.AdminUnblock(ctx, "bb")
	if err != nil || !unblocked {
		t.Fatalf("unblock: %v err=%v", unblocked, err)
	}
	if blocked, _ := s.IsBlocked("bb"); blocked {
		t.Fatal("still blocked after unblock")
	}
}

func TestCountCreatedSince(t *testing.T) {
	s := seedAdminStore(t)
	n, err := s.CountCreatedSince(t.Context(), 200)
	if err != nil || n != 2 {
		t.Fatalf("since 200: n=%d err=%v", n, err)
	}
	n, err = s.CountCreatedSince(t.Context(), 301)
	if err != nil || n != 0 {
		t.Fatalf("since 301: n=%d err=%v", n, err)
	}
}
