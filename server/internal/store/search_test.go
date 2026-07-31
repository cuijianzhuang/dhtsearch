package store

import (
	"fmt"
	"path/filepath"
	"testing"

	"dhtsearch/server/internal/filter"
)

// seedTitles inserts one row per title, newest last.
func seedTitles(t *testing.T, s *Store, titles ...string) {
	t.Helper()
	for i, name := range titles {
		if err := s.Upsert(Torrent{
			InfoHash:  fmt.Sprintf("%040x", i),
			Name:      name,
			TotalSize: 1 << 30,
			FileCount: 1,
			Files:     []filter.File{{Path: name + ".mkv", Size: 1 << 30}},
			CreatedAt: int64(1000 + i),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func hashesOf(items []Torrent) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.InfoHash
	}
	return out
}

// corpus mixes the shapes that separate the two search paths: CJK titles with
// no word boundaries, short tokens, punctuation and ad banners.
var corpus = []string{
	"三体 The Three-Body Problem S01E05 2160p WEB-DL",
	"沙丘 Dune Part Two 2024 1080p BluRay x265",
	"【高清剧集网 www.bphdtv.com】人世间 第01集 1080p",
	"Big Buck Bunny 2008 1080p",
	"Ubuntu 24.04.1 LTS Desktop amd64",
	"The Matrix 1999 4K HDR Remux",
	"流浪地球2 The Wandering Earth II 2023 2160p",
	"100% Wolf 2020 720p",
}

// The full-text index must be an accelerator, never a redefinition of what
// matches: every query has to return exactly what the plain LIKE scan returns.
func TestSearchFTSAgreesWithScan(t *testing.T) {
	s := openTest(t)
	if !s.FTSEnabled() {
		t.Skip("build without FTS5")
	}
	seedTitles(t, s, corpus...)

	queries := []string{
		"三体",             // 2 runes: invisible to trigram, LIKE must carry it
		"三",              // 1 rune
		"沙丘",             // 2 runes
		"流浪地球",           // 4 runes: indexable
		"人世间 1080p",      // short CJK + long ASCII in one query
		"Matrix",         // long ASCII
		"4K",             // 2 chars
		"bphdtv",         // only present in the ad banner
		"100%",           // LIKE wildcard must stay literal
		"dune part two",  // multi-keyword AND
		"Bunny Ubuntu",   // AND across two rows: no match
		"zzzznotpresent", // no match at all
		`the "quoted"`,   // a double quote must not break the MATCH expression
	}
	for _, q := range queries {
		s.fts = true
		withIndex, err := s.Search(t.Context(), q, 1, 20)
		if err != nil {
			t.Fatalf("Search(%q) with index: %v", q, err)
		}
		s.fts = false
		scanned, err := s.Search(t.Context(), q, 1, 20)
		if err != nil {
			t.Fatalf("Search(%q) scanning: %v", q, err)
		}
		if withIndex.Total != scanned.Total {
			t.Errorf("Search(%q): total %d with index, %d scanning",
				q, withIndex.Total, scanned.Total)
		}
		if got, want := hashesOf(withIndex.Items), hashesOf(scanned.Items); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("Search(%q): %v with index, %v scanning", q, got, want)
		}
	}
	s.fts = true
}

// A two-rune CJK query is an ordinary search here. The trigram index cannot
// see terms that short and returns nothing for them, so a regression that
// hands one to MATCH alone silently loses every result.
func TestSearchShortCJKKeyword(t *testing.T) {
	s := openTest(t)
	seedTitles(t, s, corpus...)
	for _, q := range []string{"三体", "沙丘", "4K"} {
		res, err := s.Search(t.Context(), q, 1, 20)
		if err != nil {
			t.Fatal(err)
		}
		if res.Total == 0 {
			t.Errorf("Search(%q) found nothing; short terms must fall back to scanning", q)
		}
	}
}

// Rows retitled or deleted after indexing must not linger in the index.
func TestFTSIndexTracksWrites(t *testing.T) {
	s := openTest(t)
	if !s.FTSEnabled() {
		t.Skip("build without FTS5")
	}
	seedTitles(t, s, "【www.adsite.com】Interstellar 2014 1080p")
	hash := fmt.Sprintf("%040x", 0)

	// The trimmed title becomes searchable...
	if _, err := s.SetCleanNames(map[string]string{hash: "Interstellar 2014 1080p"}); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Search(t.Context(), "Interstellar", 1, 10); err != nil || res.Total != 1 {
		t.Fatalf("after trim: total=%d err=%v, want 1", res.Total, err)
	}
	// ...and the ad text stays searchable, because name keeps the original.
	if res, err := s.Search(t.Context(), "adsite", 1, 10); err != nil || res.Total != 1 {
		t.Fatalf("original title after trim: total=%d err=%v, want 1", res.Total, err)
	}
	// A blocked row leaves the index with it.
	if _, err := s.Block([]string{hash}, []string{"x"}, "spam", 1); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Search(t.Context(), "Interstellar", 1, 10); err != nil || res.Total != 0 {
		t.Fatalf("after block: total=%d err=%v, want 0", res.Total, err)
	}
}

// Counting stops at countCap so a search's cost cannot grow with the index.
func TestSearchCountIsCapped(t *testing.T) {
	s := openTest(t)
	// One transaction: the point is the count, not the insert path.
	tx, err := s.w.Begin()
	if err != nil {
		t.Fatal(err)
	}
	ins, err := tx.Prepare(
		`INSERT INTO torrents (info_hash,name,total_size,file_count,files,created_at) VALUES (?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < countCap+50; i++ {
		if _, err := ins.Exec(fmt.Sprintf("%040x", i), fmt.Sprintf("Common Title %d", i),
			int64(1<<30), 1, []byte("[]"), int64(1000+i)); err != nil {
			t.Fatal(err)
		}
	}
	ins.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	res, err := s.Search(t.Context(), "Common", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Capped {
		t.Error("Capped = false, want true past the cap")
	}
	if res.Total != countCap {
		t.Errorf("Total = %d, want the cap %d", res.Total, countCap)
	}
	if len(res.Items) != 20 {
		t.Errorf("got %d items, want a full page of 20", len(res.Items))
	}

	// Under the cap the count stays exact and unflagged. The highest index is
	// the only title containing that digit run, so this matches exactly once.
	last := fmt.Sprintf("Common %d", countCap+49)
	res, err = s.Search(t.Context(), last, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Capped || res.Total != 1 {
		t.Errorf("narrow query %q: total=%d capped=%v, want 1/false", last, res.Total, res.Capped)
	}
}

// Known is what keeps the fetch pool off torrents the store would discard.
func TestKnown(t *testing.T) {
	s := openTest(t)
	seedTitles(t, s, "Big Buck Bunny 2008 1080p")
	indexed := fmt.Sprintf("%040x", 0)

	if ok, err := s.Known(t.Context(), indexed); err != nil || !ok {
		t.Errorf("indexed hash: known=%v err=%v, want true", ok, err)
	}
	if ok, err := s.Known(t.Context(), "ffff"); err != nil || ok {
		t.Errorf("unseen hash: known=%v err=%v, want false", ok, err)
	}
	// A blocked hash is gone from torrents but must still report known, or the
	// crawler would re-fetch it on every rediscovery.
	blocked := "aaaa"
	if _, err := s.Block([]string{blocked}, []string{"junk"}, "spam", 1); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Known(t.Context(), blocked); err != nil || !ok {
		t.Errorf("blocked hash: known=%v err=%v, want true", ok, err)
	}
}

// Existing databases predate the index, so Open has to build and backfill it.
// Rows inserted before that must come back from a search afterwards.
func TestFTSBackfillsExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.FTSEnabled() {
		t.Skip("build without FTS5")
	}
	seedTitles(t, s, corpus...)
	// Roll the database back to its pre-index shape.
	for _, stmt := range []string{
		`DROP TRIGGER torrents_fts_ai`, `DROP TRIGGER torrents_fts_ad`,
		`DROP TRIGGER torrents_fts_au`, `DROP TABLE torrents_fts`,
	} {
		if _, err := s.w.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if !s2.FTSEnabled() {
		t.Fatal("index not rebuilt on reopen")
	}
	// Backfilled rows are searchable, through terms long enough to exercise the
	// MATCH path rather than the short-keyword fallback.
	for _, q := range []string{"Matrix", "流浪地球", "Ubuntu"} {
		res, err := s2.Search(t.Context(), q, 1, 10)
		if err != nil || res.Total != 1 {
			t.Errorf("Search(%q) after backfill: total=%d err=%v, want 1", q, res.Total, err)
		}
	}
	// And writes after the rebuild keep flowing into the fresh index.
	if err := s2.Upsert(Torrent{
		InfoHash: fmt.Sprintf("%040x", len(corpus)), Name: "Arrival 2016 2160p HDR",
		TotalSize: 1 << 30, FileCount: 1, CreatedAt: 2000,
	}); err != nil {
		t.Fatal(err)
	}
	if res, err := s2.Search(t.Context(), "Arrival", 1, 10); err != nil || res.Total != 1 {
		t.Errorf("post-rebuild insert: total=%d err=%v, want 1", res.Total, err)
	}
}
