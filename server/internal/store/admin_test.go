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

func TestCreatedBuckets(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	// Window: start=1000, hourly buckets x4 -> [1000, 15400).
	// Edge rows: 999 and 15400 fall outside; 1000 lands in bucket 0.
	for i, ts := range []int64{999, 1000, 1001, 4600, 8200, 15399, 15400} {
		r := Torrent{
			InfoHash: string(rune('a'+i)) + "x", Name: "Bucket Case",
			TotalSize: 1 << 30, FileCount: 1, CreatedAt: ts,
		}
		if err := s.Upsert(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.CreatedBuckets(ctx, 1000, 3600, 4)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]int64{0: 2, 1: 1, 2: 1, 3: 1}
	if len(got) != len(want) {
		t.Fatalf("buckets = %v, want %v", got, want)
	}
	for b, n := range want {
		if got[b] != n {
			t.Fatalf("bucket %d = %d, want %d (all: %v)", b, got[b], n, got)
		}
	}
	// A window past all data is an empty (but valid) series.
	got, err = s.CreatedBuckets(ctx, 1<<40, 3600, 4)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty window: %v err=%v", got, err)
	}
}

// seedAdminSortFixture seeds ten rows spanning every admin sort and
// filter dimension: distinct sizes (three exactly on bucket
// boundaries), file counts, categories (via the largest file's
// extension), creation times, one reviewed row, and swarm snapshots
// for only some hashes — including a scraped true zero, which the
// seeders sort must rank above "no snapshot".
func seedAdminSortFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	mib, gib := int64(1<<20), int64(1<<30)
	for _, r := range []Torrent{
		{InfoHash: "v1", Name: "Video One", TotalSize: 5 * gib, FileCount: 1, CreatedAt: 100,
			Files: []filter.File{{Path: "one.mkv", Size: 5 * gib}}},
		{InfoHash: "v2", Name: "Video Two", TotalSize: 500 * mib, FileCount: 1, CreatedAt: 600,
			Files: []filter.File{{Path: "two.mkv", Size: 500 * mib}}},
		{InfoHash: "a1", Name: "Audio One", TotalSize: 50 * mib, FileCount: 3, CreatedAt: 200,
			Files: []filter.File{{Path: "a.mp3", Size: 30 * mib}, {Path: "b.mp3", Size: 10 * mib}, {Path: "c.mp3", Size: 10 * mib}}},
		{InfoHash: "d1", Name: "Doc One", TotalSize: 5 * mib, FileCount: 1, CreatedAt: 300,
			Files: []filter.File{{Path: "doc.pdf", Size: 5 * mib}}},
		{InfoHash: "s1", Name: "Soft One", TotalSize: 2 * gib, FileCount: 2, CreatedAt: 400,
			Files: []filter.File{{Path: "setup.iso", Size: 2 * gib}, {Path: "read.txt", Size: 1}}},
		{InfoHash: "z1", Name: "Arch One", TotalSize: 20 * gib, FileCount: 1, CreatedAt: 500,
			Files: []filter.File{{Path: "arch.zip", Size: 20 * gib}}},
		{InfoHash: "o1", Name: "Mystery", TotalSize: 10 * mib, FileCount: 0, CreatedAt: 700},
		{InfoHash: "b1", Name: "Boundary Ten", TotalSize: 10 * mib, FileCount: 7, CreatedAt: 800,
			Files: []filter.File{{Path: "ten.mp4", Size: 10 * mib}}},
		{InfoHash: "b2", Name: "Boundary Gig", TotalSize: gib, FileCount: 1, CreatedAt: 900,
			Files: []filter.File{{Path: "gig.mp4", Size: gib}}},
		{InfoHash: "b3", Name: "Boundary TenGig", TotalSize: 10 * gib, FileCount: 1, CreatedAt: 1000,
			Files: []filter.File{{Path: "tengig.mp4", Size: 10 * gib}}},
	} {
		if err := s.Upsert(r); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.MarkReviewed([]string{"d1"}, 500); err != nil || n != 1 {
		t.Fatalf("mark reviewed: n=%d err=%v", n, err)
	}
	if err := s.UpsertSwarmStats(ctx, []SwarmStats{
		{InfoHash: "d1", Seeders: 500, ScrapedAt: 1000},
		{InfoHash: "v1", Seeders: 50, ScrapedAt: 1000},
		{InfoHash: "s1", Seeders: 5, ScrapedAt: 1000},
		{InfoHash: "z1", Seeders: 5, ScrapedAt: 1000},
		{InfoHash: "v2", Seeders: 0, ScrapedAt: 1000}, // scraped true zero
	}); err != nil {
		t.Fatal(err)
	}
}

func adminHashes(items []AdminTorrent) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.InfoHash
	}
	return out
}

func assertAdminOrder(t *testing.T, s *Store, p AdminListParams, want []string) {
	t.Helper()
	p.Page, p.PageSize = 1, 100
	items, total, err := s.AdminListEx(t.Context(), p)
	if err != nil {
		t.Fatalf("AdminListEx(%+v): %v", p, err)
	}
	if total != len(want) {
		t.Fatalf("AdminListEx(%+v): total=%d, want %d", p, total, len(want))
	}
	got := adminHashes(items)
	if len(got) != len(want) {
		t.Fatalf("AdminListEx(%+v): got %v, want %v", p, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AdminListEx(%+v): got %v, want %v", p, got, want)
		}
	}
	for _, it := range items {
		if it.Files != nil {
			t.Fatalf("list must not load files: %+v", it)
		}
	}
}

// assertAdminSortFilter runs the full sort/filter matrix against one
// store; the SQLite test and the Postgres parity test share it so
// both backends are held to byte-identical expectations.
func assertAdminSortFilter(t *testing.T, s *Store) {
	t.Helper()

	// Sorts (unfiltered: the SQL path for all but seeders).
	assertAdminOrder(t, s, AdminListParams{Sort: AdminSortNewest},
		[]string{"b3", "b2", "b1", "o1", "v2", "z1", "s1", "d1", "a1", "v1"})
	assertAdminOrder(t, s, AdminListParams{Sort: ""}, // "" defaults to newest
		[]string{"b3", "b2", "b1", "o1", "v2", "z1", "s1", "d1", "a1", "v1"})
	assertAdminOrder(t, s, AdminListParams{Sort: AdminSortOldest},
		[]string{"v1", "a1", "d1", "s1", "z1", "v2", "o1", "b1", "b2", "b3"})
	assertAdminOrder(t, s, AdminListParams{Sort: AdminSortSizeDesc},
		[]string{"z1", "b3", "v1", "s1", "b2", "v2", "a1", "b1", "o1", "d1"})
	assertAdminOrder(t, s, AdminListParams{Sort: AdminSortSizeAsc},
		[]string{"d1", "b1", "o1", "a1", "v2", "b2", "s1", "v1", "b3", "z1"})
	assertAdminOrder(t, s, AdminListParams{Sort: AdminSortFilesDesc},
		[]string{"b1", "a1", "s1", "b3", "b2", "v2", "z1", "d1", "v1", "o1"})
	// Seeders: snapshot counts first (500/50/5/5 by created_at, then
	// the scraped true zero), no-snapshot rows last by created_at.
	assertAdminOrder(t, s, AdminListParams{Sort: AdminSortSeeders},
		[]string{"d1", "v1", "z1", "s1", "v2", "b3", "b2", "b1", "o1", "a1"})

	// Category filter (the in-memory path), newest first.
	assertAdminOrder(t, s, AdminListParams{Category: CategoryVideo},
		[]string{"b3", "b2", "b1", "v2", "v1"})
	assertAdminOrder(t, s, AdminListParams{Category: CategoryAudio}, []string{"a1"})
	assertAdminOrder(t, s, AdminListParams{Category: CategoryDoc}, []string{"d1"})
	assertAdminOrder(t, s, AdminListParams{Category: CategorySoftware}, []string{"s1"})
	assertAdminOrder(t, s, AdminListParams{Category: CategoryArchive}, []string{"z1"})
	assertAdminOrder(t, s, AdminListParams{Category: CategoryOther}, []string{"o1"})
	assertAdminOrder(t, s, AdminListParams{Category: CategoryImage}, []string{})

	// The in-memory path must preserve the SQL path's relative order:
	// size_desc restricted to videos equals the filtered query.
	assertAdminOrder(t, s, AdminListParams{Category: CategoryVideo, Sort: AdminSortSizeDesc},
		[]string{"b3", "v1", "b2", "v2", "b1"})
	// Category + seeders combine too.
	assertAdminOrder(t, s, AdminListParams{Category: CategoryVideo, Sort: AdminSortSeeders},
		[]string{"v1", "v2", "b3", "b2", "b1"})

	// Size buckets: boundaries are min-inclusive (10 MiB lands in
	// 10m_100m, 1 GiB in 1g_10g, 10 GiB in gt10g).
	assertAdminOrder(t, s, AdminListParams{Size: "lt10m"}, []string{"d1"})
	assertAdminOrder(t, s, AdminListParams{Size: "10m_100m"}, []string{"b1", "o1", "a1"})
	assertAdminOrder(t, s, AdminListParams{Size: "100m_1g"}, []string{"v2"})
	assertAdminOrder(t, s, AdminListParams{Size: "1g_10g"}, []string{"b2", "s1", "v1"})
	assertAdminOrder(t, s, AdminListParams{Size: "gt10g"}, []string{"b3", "z1"})

	// Filters combine with AND across q, status, category and size.
	assertAdminOrder(t, s, AdminListParams{Query: "video", Category: CategoryVideo},
		[]string{"v2", "v1"})
	assertAdminOrder(t, s, AdminListParams{Status: AdminStatusUnreviewed, Category: CategoryVideo, Size: "1g_10g"},
		[]string{"b2", "v1"})
	assertAdminOrder(t, s, AdminListParams{Status: AdminStatusReviewed}, []string{"d1"})
	assertAdminOrder(t, s, AdminListParams{Status: AdminStatusReviewed, Sort: AdminSortSeeders},
		[]string{"d1"})

	// Invalid values fall back to defaults instead of erroring.
	assertAdminOrder(t, s, AdminListParams{Sort: "bogus", Category: "bogus", Size: "bogus"},
		[]string{"b3", "b2", "b1", "o1", "v2", "z1", "s1", "d1", "a1", "v1"})

	// Pagination cuts the in-memory path the same way: page 2 of the
	// video category at 2 per page.
	items, total, err := s.AdminListEx(t.Context(), AdminListParams{
		Category: CategoryVideo, Page: 2, PageSize: 2,
	})
	if err != nil || total != 5 || len(items) != 2 ||
		items[0].InfoHash != "b1" || items[1].InfoHash != "v2" {
		t.Fatalf("video page 2: items=%v total=%d err=%v", adminHashes(items), total, err)
	}
	// A page past the end is empty, not an error.
	items, total, err = s.AdminListEx(t.Context(), AdminListParams{
		Category: CategoryVideo, Page: 99, PageSize: 2,
	})
	if err != nil || total != 5 || len(items) != 0 {
		t.Fatalf("video page 99: items=%v total=%d err=%v", adminHashes(items), total, err)
	}
}

func TestAdminListSortFilter(t *testing.T) {
	s := openTest(t)
	seedAdminSortFixture(t, s)
	assertAdminSortFilter(t, s)
}
