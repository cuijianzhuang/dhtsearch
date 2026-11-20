package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/store"
)

const filesFixtureHash = "ffffffffffffffffffffffffffffffffffffffff"

// filesFixture serves the API over a store holding one 300-file
// torrent (plus nothing else), with the admin token enabled.
func filesFixture(t *testing.T) string {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tor := store.Torrent{
		InfoHash:  filesFixtureHash,
		Name:      "Files Fixture BigPack",
		FileCount: 300,
		CreatedAt: 500,
	}
	for i := 0; i < 300; i++ {
		tor.Files = append(tor.Files, filter.File{
			Path: fmt.Sprintf("pack/file-%04d.bin", i),
			Size: int64(1000 + i),
		})
		tor.TotalSize += int64(1000 + i)
	}
	if err := st.Upsert(tor); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, nil, nil, Options{AdminToken: testAdminToken}).Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

type filesPage struct {
	InfoHash   string `json:"info_hash"`
	Total      int    `json:"total"`
	TotalAll   int    `json:"total_all"`
	Page       int    `json:"page"`
	PageSize   int    `json:"page_size"`
	TotalPages int    `json:"total_pages"`
	Files      []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	} `json:"files"`
}

func getFilesPage(t *testing.T, url, token string) (int, filesPage) {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var p filesPage
	if resp.StatusCode == 200 {
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, p
}

func TestPublicTorrentFiles(t *testing.T) {
	base := filesFixture(t)
	url := base + "/api/torrents/" + filesFixtureHash + "/files"

	code, p := getFilesPage(t, url, "")
	if code != 200 {
		t.Fatalf("page 1: status %d", code)
	}
	if p.Total != 300 || p.Page != 1 || p.PageSize != 100 || p.TotalPages != 3 {
		t.Fatalf("page 1 header: %+v", p)
	}
	if len(p.Files) != 100 || p.Files[0].Path != "pack/file-0000.bin" || p.Files[99].Path != "pack/file-0099.bin" {
		t.Fatalf("page 1 files: %d entries, first/last %q/%q", len(p.Files), p.Files[0].Path, p.Files[len(p.Files)-1].Path)
	}

	code, p = getFilesPage(t, url+"?page=3", "")
	if code != 200 || len(p.Files) != 100 || p.Files[0].Path != "pack/file-0200.bin" {
		t.Fatalf("page 3: code %d, %d files", code, len(p.Files))
	}

	// Past the end: empty page, real total.
	code, p = getFilesPage(t, url+"?page=4", "")
	if code != 200 || len(p.Files) != 0 || p.Total != 300 {
		t.Fatalf("page 4: code %d, %d files, total %d", code, len(p.Files), p.Total)
	}

	// page_size is capped at 500.
	code, p = getFilesPage(t, url+"?page_size=9999", "")
	if code != 200 || p.PageSize != 500 || len(p.Files) != 300 || p.TotalPages != 1 {
		t.Fatalf("capped page_size: code %d, %+v, %d files", code, p, len(p.Files))
	}

	// Garbage paging params fall back to the defaults.
	code, p = getFilesPage(t, url+"?page=abc&page_size=-2", "")
	if code != 200 || p.Page != 1 || p.PageSize != 100 {
		t.Fatalf("garbage params: code %d, page %d size %d", code, p.Page, p.PageSize)
	}

	// Unknown hash 404s.
	code, _ = getFilesPage(t, base+"/api/torrents/"+strings.Repeat("0", 40)+"/files", "")
	if code != http.StatusNotFound {
		t.Fatalf("unknown hash: status %d, want 404", code)
	}

	// Without q, total_all mirrors total.
	code, p = getFilesPage(t, url, "")
	if code != 200 || p.TotalAll != 300 {
		t.Fatalf("unfiltered: code %d, total_all %d, want 200 / 300", code, p.TotalAll)
	}

	// ?q= filters by case-insensitive path substring before paging:
	// "FILE-02" matches file-0200..file-0299, total becomes the
	// filtered count and total_all keeps the full stored count.
	code, p = getFilesPage(t, url+"?q=FILE-02", "")
	if code != 200 {
		t.Fatalf("filtered: status %d", code)
	}
	if p.Total != 100 || p.TotalAll != 300 || p.TotalPages != 1 {
		t.Fatalf("filtered header: %+v", p)
	}
	if len(p.Files) != 100 || p.Files[0].Path != "pack/file-0200.bin" || p.Files[99].Path != "pack/file-0299.bin" {
		t.Fatalf("filtered files: %d entries", len(p.Files))
	}
}

func TestAdminTorrentFiles(t *testing.T) {
	base := filesFixture(t)
	url := base + "/api/admin/torrents/" + filesFixtureHash + "/files"

	if code, _ := getFilesPage(t, url, ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", code)
	}
	code, p := getFilesPage(t, url+"?page=2", testAdminToken)
	if code != 200 {
		t.Fatalf("page 2: status %d", code)
	}
	if p.Total != 300 || len(p.Files) != 100 || p.Files[0].Path != "pack/file-0100.bin" {
		t.Fatalf("page 2: total %d, %d files, first %q", p.Total, len(p.Files), p.Files[0].Path)
	}
}

func TestAdminDetailOmitsFiles(t *testing.T) {
	base := filesFixture(t)
	code, m := adminReq(t, "GET", base+"/api/admin/torrents/"+filesFixtureHash, testAdminToken, nil)
	if code != 200 {
		t.Fatalf("detail: status %d", code)
	}
	if _, ok := m["files"]; ok {
		t.Fatalf("detail still embeds the file list: %v", m["files"])
	}
	if got := m["file_count"]; got != float64(300) {
		t.Fatalf("file_count = %v, want 300", got)
	}
}

func TestSearchCapsEmbeddedFiles(t *testing.T) {
	base := filesFixture(t)
	m := getJSON(t, base+"/api/search?q="+strings.ReplaceAll("Files Fixture", " ", "%20"))
	results, ok := m["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results: %v", m["results"])
	}
	item := results[0].(map[string]any)
	files, _ := item["files"].([]any)
	if len(files) != maxEmbeddedFiles {
		t.Fatalf("embedded files = %d, want capped at %d", len(files), maxEmbeddedFiles)
	}
	if got := item["file_count"]; got != float64(300) {
		t.Fatalf("file_count = %v, want 300 (true total preserved)", got)
	}
}
