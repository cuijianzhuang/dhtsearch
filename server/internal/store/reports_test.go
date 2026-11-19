package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"dhtsearch/server/internal/filter"
)

func TestReportsRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hash := strings.Repeat("a", 40)
	if err := s.Upsert(Torrent{
		InfoHash: hash, Name: "Some Movie 2024", TotalSize: 1 << 30, FileCount: 1, CreatedAt: 100,
		Files: []filter.File{{Path: "movie.mkv", Size: 1 << 30}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddReport(ctx, hash, "adult", "looks wrong", "203.0.113.7", 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.AddReport(ctx, hash, "spam", "", "198.51.100.2", 2000); err != nil {
		t.Fatal(err)
	}

	n, err := s.PendingReportsCount(ctx)
	if err != nil || n != 2 {
		t.Fatalf("pending = %d, %v; want 2", n, err)
	}
	items, total, err := s.AdminListReports(ctx, ReportPending, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("list: total=%d items=%d, want 2/2", total, len(items))
	}
	// Newest first, display name joined in.
	if items[0].Reason != "spam" || items[0].Name != "Some Movie 2024" {
		t.Errorf("first item = %+v", items[0])
	}
	first := items[0]

	rep, err := s.ReportByID(ctx, first.ID)
	if err != nil || rep == nil || rep.InfoHash != hash {
		t.Fatalf("ReportByID: %v / %v", rep, err)
	}
	ok, err := s.AdminSetReportStatus(ctx, first.ID, ReportDismissed)
	if err != nil || !ok {
		t.Fatalf("set status: %v / %v", ok, err)
	}
	if ok, _ := s.AdminSetReportStatus(ctx, 99999, ReportResolved); ok {
		t.Error("set status on missing report reported success")
	}
	n, _ = s.PendingReportsCount(ctx)
	if n != 1 {
		t.Fatalf("pending after dismiss = %d, want 1", n)
	}
	// The dismissed report shows under its own filter and under all.
	if _, total, _ := s.AdminListReports(ctx, ReportDismissed, 1, 20); total != 1 {
		t.Errorf("dismissed total = %d, want 1", total)
	}
	if _, total, _ := s.AdminListReports(ctx, ReportStatusAll, 1, 20); total != 2 {
		t.Errorf("all total = %d, want 2", total)
	}
	// After the torrent is blocked (deleted), the report keeps listing
	// with an empty name instead of breaking.
	if _, err := s.Block([]string{hash}, []string{"Some Movie 2024"}, "report:spam", 3000); err != nil {
		t.Fatal(err)
	}
	items, _, err = s.AdminListReports(ctx, ReportPending, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "" {
		t.Errorf("after block: %+v, want one item with empty name", items)
	}
}

// TestPostgresReportsAndSearchEx runs the reports table and the
// category search against real Postgres when
// DHTSEARCH_TEST_POSTGRES_URL points at a throwaway database (the
// database is reset first, like TestPostgresStore).
func TestPostgresReportsAndSearchEx(t *testing.T) {
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
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS torrents`, `DROP TABLE IF EXISTS stats`,
		`DROP TABLE IF EXISTS blocked`, `DROP TABLE IF EXISTS settings`,
		`DROP TABLE IF EXISTS reports`,
	} {
		if _, err := admin.Exec(stmt); err != nil {
			t.Fatalf("reset test database: %v", err)
		}
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	hash := strings.Repeat("a", 40)
	if err := s.Upsert(Torrent{
		InfoHash: hash, Name: "PG Movie", TotalSize: 2 << 30, FileCount: 1, CreatedAt: 100,
		Files: []filter.File{{Path: "pg.mkv", Size: 2 << 30}},
	}); err != nil {
		t.Fatal(err)
	}
	items, total, counts, err := s.SearchEx(ctx, SearchParams{Category: CategoryVideo, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || counts[CategoryVideo] != 1 {
		t.Fatalf("SearchEx on PG: total=%d items=%d counts=%v", total, len(items), counts)
	}
	if err := s.AddReport(ctx, hash, "infringement", "", "192.0.2.1", 1000); err != nil {
		t.Fatal(err)
	}
	reps, total, err := s.AdminListReports(ctx, ReportPending, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(reps) != 1 || reps[0].ID < 1 || reps[0].Name != "PG Movie" {
		t.Fatalf("reports on PG: total=%d reps=%+v", total, reps)
	}
	if n, err := s.PendingReportsCount(ctx); err != nil || n != 1 {
		t.Fatalf("pending on PG: %d, %v", n, err)
	}
}
