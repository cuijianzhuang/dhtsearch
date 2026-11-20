package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	"dhtsearch/server/internal/filter"
)

// Ranking fixtures for the query "big buck bunny". Every row satisfies
// the SQL AND-match (each keyword occurs in the name or the alias); the
// rows differ only in match quality, and the expected relevance order
// is the order of this list:
//
//	exact        the name IS the phrase
//	prefix       phrase at the start, short tail
//	junk         phrase at the start, long keyword-stuffed tail
//	inorder      all keywords in the name, in order, but not contiguous
//	aliaspart    one keyword only in the alias (which holds the phrase);
//	             newest row of the set — freshness must not lift it
//	reversed     all keywords in the name, wrong order; second newest
//	aliasonly    keywords only in the alias, the name matches nothing
var rankingFixtures = []struct {
	key     string
	name    string
	alias   string
	created int64
}{
	{"exact", "Big Buck Bunny", "", 100},
	{"prefix", "Big Buck Bunny 2008", "", 90},
	{"junk", "Big Buck Bunny 2008 BluRay 1080p x264 REMUX TrueHD Atmos DD+ 5.1 H264-FLUX", "", 95},
	{"inorder", "The Big Buck And Bunny Show", "", 80},
	{"aliaspart", "Big Bunny Adventure", "Big Buck Bunny 完整版", 300},
	{"reversed", "Bunny Buck Big", "", 200},
	{"aliasonly", "Romaji Title Desu", "Big Buck Bunny", 50},
}

func hashOf(key string) string {
	// Deterministic 40-hex-char hashes, ordered so the info_hash
	// tiebreak can never be what produces the expected order.
	m := map[string]string{
		"exact": "f1", "prefix": "f2", "junk": "f3", "inorder": "f4",
		"aliaspart": "f5", "reversed": "f6", "aliasonly": "f7",
		"ub-ub-exact": "e1", "ub-prefix": "e2", "ub-junk": "e3", "ub-mid": "e4",
	}
	return strings.Repeat("0", 38) + m[key]
}

func seedRankingFixtures(t *testing.T, s *Store) {
	t.Helper()
	for _, f := range rankingFixtures {
		rec := Torrent{
			InfoHash: hashOf(f.key), Name: f.name, Alias: f.alias,
			TotalSize: 1 << 30, FileCount: 1, CreatedAt: f.created,
			Files: []filter.File{{Path: "file.mkv", Size: 1 << 30}},
		}
		if err := s.Upsert(rec); err != nil {
			t.Fatal(err)
		}
	}
}

// checkRankingOrder asserts the relevance order of the fixtures on any
// backend; it is shared by the SQLite test and the Postgres gate so the
// two dialects are held to exactly the same order.
func checkRankingOrder(t *testing.T, s *Store) {
	t.Helper()
	items, total, _, err := s.SearchEx(context.Background(), SearchParams{
		Query: "big buck bunny", Sort: SortRelevance, Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != len(rankingFixtures) || len(items) != len(rankingFixtures) {
		t.Fatalf("total=%d items=%d, want %d", total, len(items), len(rankingFixtures))
	}
	want := make([]string, len(rankingFixtures))
	for i, f := range rankingFixtures {
		want[i] = f.name
	}
	got := make([]string, len(items))
	for i, it := range items {
		got[i] = it.Name
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("relevance order:\n got %q\nwant %q", got, want)
		}
	}

	// The order must survive paging: page 2 (size 3) continues the
	// same sequence rather than reshuffling.
	page2, _, _, err := s.SearchEx(context.Background(), SearchParams{
		Query: "big buck bunny", Sort: SortRelevance, Page: 2, PageSize: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 3 || page2[0].Name != want[3] || page2[2].Name != want[5] {
		names := []string{}
		for _, it := range page2 {
			names = append(names, it.Name)
		}
		t.Fatalf("page 2 = %q, want %q", names, want[3:6])
	}

	// newest/size are untouched by scoring.
	byNewest, _, _, err := s.SearchEx(context.Background(), SearchParams{
		Query: "big buck bunny", Sort: SortNewest, Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if byNewest[0].CreatedAt != 300 || byNewest[1].CreatedAt != 200 {
		t.Errorf("newest order changed: first two created_at = %d, %d",
			byNewest[0].CreatedAt, byNewest[1].CreatedAt)
	}
}

func TestSearchRankingOrder(t *testing.T) {
	s := openTest(t)
	seedRankingFixtures(t, s)
	checkRankingOrder(t, s)
}

// TestSearchRankingSingleKeyword: one term, so phrase == prefix ==
// the keyword; exact name first, then short prefix hits, then a long
// keyword-stuffed prefix hit, then a middle hit.
func TestSearchRankingSingleKeyword(t *testing.T) {
	s := openTest(t)
	recs := []Torrent{
		{InfoHash: hashOf("ub-mid"), Name: "The Ubuntu Handbook", TotalSize: 1, FileCount: 1, CreatedAt: 40},
		{InfoHash: hashOf("ub-junk"), Name: "Ubuntu Super Mega Junk Pack With Extra Words And More Stuff", TotalSize: 1, FileCount: 1, CreatedAt: 30},
		{InfoHash: hashOf("ub-prefix"), Name: "Ubuntu 24.04 LTS", TotalSize: 1, FileCount: 1, CreatedAt: 20},
		{InfoHash: hashOf("ub-exact"), Name: "Ubuntu", TotalSize: 1, FileCount: 1, CreatedAt: 10},
	}
	for _, r := range recs {
		if err := s.Upsert(r); err != nil {
			t.Fatal(err)
		}
	}
	items, total, _, err := s.SearchEx(context.Background(), SearchParams{
		Query: "ubuntu", Sort: SortRelevance, Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}
	want := []string{"Ubuntu", "Ubuntu 24.04 LTS",
		"Ubuntu Super Mega Junk Pack With Extra Words And More Stuff", "The Ubuntu Handbook"}
	for i, w := range want {
		if items[i].Name != w {
			t.Fatalf("order[%d] = %q, want %q (full: %v)", i, items[i].Name, w, items)
		}
	}
}

// TestSearchRankingAliasOnlyRelative: when every match comes through
// the alias (romaji name, Chinese alias — the dmhy pattern), the alias
// score still orders them: exact-phrase alias beats scrambled alias.
func TestSearchRankingAliasOnlyRelative(t *testing.T) {
	s := openTest(t)
	recs := []Torrent{
		{InfoHash: strings.Repeat("1", 40), Name: "Romaji One", Alias: "Bunny Buck Big", TotalSize: 1, FileCount: 1, CreatedAt: 999},
		{InfoHash: strings.Repeat("2", 40), Name: "Romaji Two", Alias: "Big Buck Bunny", TotalSize: 1, FileCount: 1, CreatedAt: 1},
	}
	for _, r := range recs {
		if err := s.Upsert(r); err != nil {
			t.Fatal(err)
		}
	}
	items, _, _, err := s.SearchEx(context.Background(), SearchParams{
		Query: "big buck bunny", Sort: SortRelevance, Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "Romaji Two" {
		t.Fatalf("alias-only order = %v, want Romaji Two (exact alias) first", items)
	}
}

// TestPostgresSearchRanking holds Postgres to the same relevance order
// as SQLite when DHTSEARCH_TEST_POSTGRES_URL points at a throwaway
// database (the test drops and recreates the torrents table).
func TestPostgresSearchRanking(t *testing.T) {
	url := os.Getenv("DHTSEARCH_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("DHTSEARCH_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	if !IsPostgresURL(url) {
		t.Fatalf("DHTSEARCH_TEST_POSTGRES_URL is not a postgres URL")
	}
	admin, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`DROP TABLE IF EXISTS torrents`); err != nil {
		t.Fatalf("reset test database: %v", err)
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	seedRankingFixtures(t, s)
	checkRankingOrder(t, s)
}

// BenchmarkSearchExRelevance seeds 20k torrents, ~40% of which match
// the benchmark query, and times a relevance search end to end (SQL
// match + decompress + classify + score + sort + page).
func BenchmarkSearchExRelevance(b *testing.B) {
	s, err := Open(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	templates := []string{
		"Big Buck Bunny %d",
		"Big Buck Bunny %d 1080p BluRay x264 REMUX TrueHD",
		"The Big Buck And Bunny Show %d",
		"Bunny Buck Big Collection %d",
		"Big Buck Bunny %d Super Long Junk Name With Many Extra Words And Keywords",
		"Big Bunny Adventure %d",
		"Ubuntu 24.04 LTS %d",
		"Sintel Open Movie %d",
		"Random Documentary %d",
		"Some Music Album %d",
	}
	for i := 0; i < 20000; i++ {
		name := fmt.Sprintf(templates[i%len(templates)], i)
		rec := Torrent{
			InfoHash: fmt.Sprintf("%040x", i+1), Name: name,
			TotalSize: int64(i + 1), FileCount: 1, CreatedAt: int64(i),
			Files: []filter.File{{Path: "f.mkv", Size: 1}},
		}
		if i%len(templates) == 5 {
			rec.Alias = "Big Buck Bunny 完整版"
		}
		if err := s.Upsert(rec); err != nil {
			b.Fatal(err)
		}
	}
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		_, total, _, err := s.SearchEx(ctx, SearchParams{
			Query: "big buck bunny", Sort: SortRelevance, Page: 1, PageSize: 20,
		})
		if err != nil {
			b.Fatal(err)
		}
		if total == 0 {
			b.Fatal("no matches")
		}
	}
}
