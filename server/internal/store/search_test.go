package store

import (
	"context"
	"strings"
	"testing"

	"dhtsearch/server/internal/filter"
)

// seedSearchEx inserts a small mixed-category library:
//
//	aa Movie Alpha 2024      video  2 GiB  t=100
//	bb Movie Beta 2024       video  1 GiB  t=200
//	cc Alpha Song Collection audio  3 GiB  t=300
//	dd Ubuntu ISO            software 5 GiB t=400
//	ee Random Text Files     doc    1 GiB  t=500
func seedSearchEx(t *testing.T) *Store {
	t.Helper()
	s := openTest(t)
	recs := []Torrent{
		{InfoHash: strings.Repeat("a", 40), Name: "Movie Alpha 2024", TotalSize: 2 << 30, FileCount: 1, CreatedAt: 100,
			Files: []filter.File{{Path: "alpha.mkv", Size: 2 << 30}}},
		{InfoHash: strings.Repeat("b", 40), Name: "Movie Beta 2024", TotalSize: 1 << 30, FileCount: 1, CreatedAt: 200,
			Files: []filter.File{{Path: "beta.mp4", Size: 1 << 30}}},
		{InfoHash: strings.Repeat("c", 40), Name: "Alpha Song Collection", TotalSize: 3 << 30, FileCount: 1, CreatedAt: 300,
			Files: []filter.File{{Path: "song.flac", Size: 3 << 30}}},
		{InfoHash: strings.Repeat("d", 40), Name: "Ubuntu ISO", TotalSize: 5 << 30, FileCount: 1, CreatedAt: 400,
			Files: []filter.File{{Path: "ubuntu.iso", Size: 5 << 30}}},
		{InfoHash: strings.Repeat("e", 40), Name: "Random Text Files", TotalSize: 1 << 30, FileCount: 1, CreatedAt: 500,
			Files: []filter.File{{Path: "notes.pdf", Size: 1 << 30}}},
	}
	for _, r := range recs {
		if err := s.Upsert(r); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestSearchExCountsAndFilter(t *testing.T) {
	s := seedSearchEx(t)
	ctx := context.Background()

	// No filter: counts cover the whole library, total matches.
	_, total, counts, err := s.SearchEx(ctx, SearchParams{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	want := map[string]int{CategoryVideo: 2, CategoryAudio: 1, CategorySoftware: 1, CategoryDoc: 1,
		CategoryImage: 0, CategoryArchive: 0, CategoryOther: 0}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("counts[%s] = %d, want %d", k, counts[k], v)
		}
	}

	// Category filter narrows items+total but not the counts.
	items, total, counts, err := s.SearchEx(ctx, SearchParams{Category: CategoryVideo, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("video: total=%d items=%d, want 2/2", total, len(items))
	}
	for _, it := range items {
		if it.Category != CategoryVideo {
			t.Errorf("filtered item has category %s", it.Category)
		}
	}
	if counts[CategoryAudio] != 1 {
		t.Errorf("counts changed under filter: %v", counts)
	}

	// Keyword + category: counts cover the keyword matches only.
	_, total, counts, err = s.SearchEx(ctx, SearchParams{Query: "alpha", Category: CategoryAudio, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("alpha+audio total = %d, want 1", total)
	}
	if counts[CategoryVideo] != 1 || counts[CategoryAudio] != 1 {
		t.Errorf("alpha counts = %v, want video:1 audio:1", counts)
	}
}

func TestSearchExSortSize(t *testing.T) {
	s := seedSearchEx(t)
	items, _, _, err := s.SearchEx(context.Background(), SearchParams{Sort: SortSize, Page: 1, PageSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("page size 3 returned %d", len(items))
	}
	wantSizes := []int64{5 << 30, 3 << 30, 2 << 30}
	for i, ws := range wantSizes {
		if items[i].TotalSize != ws {
			t.Errorf("items[%d] size = %d, want %d", i, items[i].TotalSize, ws)
		}
	}
	// Default order is newest first.
	items, _, _, err = s.SearchEx(context.Background(), SearchParams{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].CreatedAt != 500 || items[1].CreatedAt != 400 {
		t.Errorf("default order not newest-first: %d, %d", items[0].CreatedAt, items[1].CreatedAt)
	}
	// Page beyond the end is empty, not an error.
	items, total, _, err := s.SearchEx(context.Background(), SearchParams{Page: 99, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 || total != 5 {
		t.Errorf("page 99: items=%d total=%d, want 0/5", len(items), total)
	}
}

func TestGetAndSimilar(t *testing.T) {
	s := seedSearchEx(t)
	ctx := context.Background()
	hashA := strings.Repeat("a", 40)

	got, err := s.Get(ctx, hashA)
	if err != nil || got == nil {
		t.Fatalf("Get: %v / %v", got, err)
	}
	if got.Category != CategoryVideo {
		t.Errorf("Get category = %s, want video", got.Category)
	}
	missing, err := s.Get(ctx, strings.Repeat("f", 40))
	if err != nil || missing != nil {
		t.Fatalf("Get missing: %v / %v", missing, err)
	}

	sim, err := s.Similar(ctx, hashA, 6)
	if err != nil {
		t.Fatal(err)
	}
	// "Movie Beta 2024" shares the token "movie" and the category.
	if len(sim) == 0 {
		t.Fatal("Similar returned nothing")
	}
	if sim[0].InfoHash != strings.Repeat("b", 40) {
		t.Errorf("first similar = %s, want Movie Beta (same category)", sim[0].Name)
	}
	for _, it := range sim {
		if it.InfoHash == hashA {
			t.Error("Similar included the torrent itself")
		}
	}
}
