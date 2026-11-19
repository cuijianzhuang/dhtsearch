package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/store"
)

const testAdminToken = "test-admin-token"

// adminFixture seeds three torrents and serves the API with the given
// admin token ("" = admin disabled).
func adminFixture(t *testing.T, token string) (string, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for i, name := range []string{"Big Buck Bunny", "Sintel", "Ubuntu 24.04"} {
		if err := st.Upsert(store.Torrent{
			InfoHash:  strings.Repeat(string(rune('a'+i)), 40),
			Name:      name,
			TotalSize: int64(1+i) << 30,
			FileCount: 1,
			Files:     []filter.File{{Path: name + ".mkv", Size: int64(1+i) << 30}},
			CreatedAt: int64(100 + i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(New(st, nil, nil, Options{AdminToken: token}).Handler())
	t.Cleanup(srv.Close)
	return srv.URL, st
}

func adminReq(t *testing.T, method, url, token string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, rd)
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
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestAdminDisabledWithoutToken(t *testing.T) {
	base, _ := adminFixture(t, "")
	if code, _ := adminReq(t, "GET", base+"/api/admin/overview", "", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("overview with admin disabled: %d", code)
	}
	if code, _ := adminReq(t, "POST", base+"/api/admin/login", "", map[string]string{"token": "x"}); code != http.StatusServiceUnavailable {
		t.Fatalf("login with admin disabled: %d", code)
	}
	// Public API unaffected.
	if code, _ := adminReq(t, "GET", base+"/api/healthz", "", nil); code != 200 {
		t.Fatalf("healthz: %d", code)
	}
}

func TestAdminAuthRequired(t *testing.T) {
	base, _ := adminFixture(t, testAdminToken)
	if code, _ := adminReq(t, "GET", base+"/api/admin/overview", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := adminReq(t, "GET", base+"/api/admin/overview", "wrong", nil); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	if code, m := adminReq(t, "GET", base+"/api/admin/overview", testAdminToken, nil); code != 200 {
		t.Fatalf("good token: %d %v", code, m)
	} else if m["torrents"].(float64) != 3 || m["pending_review"].(float64) != 3 || m["blocked"].(float64) != 0 {
		t.Fatalf("overview numbers: %v", m)
	}
}

func TestAdminLogin(t *testing.T) {
	base, _ := adminFixture(t, testAdminToken)
	if code, _ := adminReq(t, "POST", base+"/api/admin/login", "", map[string]string{"token": "nope"}); code != http.StatusUnauthorized {
		t.Fatalf("bad login: %d", code)
	}
	if code, _ := adminReq(t, "POST", base+"/api/admin/login", "", map[string]string{"token": testAdminToken}); code != 200 {
		t.Fatalf("good login: %d", code)
	}
}

// Login attempts are throttled well below the general API rate: a burst
// of bad guesses must start drawing 429s.
func TestAdminLoginRateLimited(t *testing.T) {
	base, _ := adminFixture(t, testAdminToken)
	got429 := false
	for i := 0; i < 10; i++ {
		code, _ := adminReq(t, "POST", base+"/api/admin/login", "", map[string]string{"token": "nope"})
		if code == http.StatusTooManyRequests {
			got429 = true
			break
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if !got429 {
		t.Fatal("login attempts were never rate limited")
	}
}

func TestAdminListAndReviewFlow(t *testing.T) {
	base, _ := adminFixture(t, testAdminToken)
	hashA := strings.Repeat("a", 40)

	code, m := adminReq(t, "GET", base+"/api/admin/torrents?page_size=2&page=1", testAdminToken, nil)
	if code != 200 || m["total"].(float64) != 3 || len(m["results"].([]any)) != 2 {
		t.Fatalf("list p1: %d %v", code, m)
	}
	code, m = adminReq(t, "GET", base+"/api/admin/torrents?page_size=2&page=2", testAdminToken, nil)
	if code != 200 || len(m["results"].([]any)) != 1 {
		t.Fatalf("list p2: %d %v", code, m)
	}
	// Keyword filter.
	_, m = adminReq(t, "GET", base+"/api/admin/torrents?q=sintel", testAdminToken, nil)
	if m["total"].(float64) != 1 {
		t.Fatalf("keyword list: %v", m)
	}
	// Detail carries the file list.
	code, m = adminReq(t, "GET", base+"/api/admin/torrents/"+hashA, testAdminToken, nil)
	if code != 200 || len(m["files"].([]any)) != 1 {
		t.Fatalf("detail: %d %v", code, m)
	}
	if code, _ := adminReq(t, "GET", base+"/api/admin/torrents/"+strings.Repeat("z", 40), testAdminToken, nil); code != http.StatusNotFound {
		t.Fatalf("missing detail: %d", code)
	}
	// Invalid status is a 400, not a silent all.
	if code, _ := adminReq(t, "GET", base+"/api/admin/torrents?status=bogus", testAdminToken, nil); code != http.StatusBadRequest {
		t.Fatalf("bad status: %d", code)
	}

	// Approve one, then the status filters split 1/2.
	if code, _ := adminReq(t, "POST", base+"/api/admin/torrents/"+hashA+"/approve", testAdminToken, nil); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	_, m = adminReq(t, "GET", base+"/api/admin/torrents?status=unreviewed", testAdminToken, nil)
	if m["total"].(float64) != 2 {
		t.Fatalf("unreviewed after approve: %v", m)
	}
	_, m = adminReq(t, "GET", base+"/api/admin/torrents?status=reviewed", testAdminToken, nil)
	if m["total"].(float64) != 1 {
		t.Fatalf("reviewed after approve: %v", m)
	}
}

func TestAdminBlockUnblockAndDelete(t *testing.T) {
	base, st := adminFixture(t, testAdminToken)
	hashB := strings.Repeat("b", 40)
	hashC := strings.Repeat("c", 40)

	// Block removes the torrent and records it; re-adding is refused.
	if code, _ := adminReq(t, "POST", base+"/api/admin/torrents/"+hashB+"/block", testAdminToken, map[string]string{"reason": "spam"}); code != 200 {
		t.Fatalf("block: %d", code)
	}
	if blocked, _ := st.IsBlocked(hashB); !blocked {
		t.Fatal("hash not in blocked table after block")
	}
	_, m := adminReq(t, "GET", base+"/api/admin/torrents", testAdminToken, nil)
	if m["total"].(float64) != 2 {
		t.Fatalf("list after block: %v", m)
	}
	_, m = adminReq(t, "GET", base+"/api/admin/blocked", testAdminToken, nil)
	if m["total"].(float64) != 1 || m["results"].([]any)[0].(map[string]any)["reason"] != "spam" {
		t.Fatalf("blocked list: %v", m)
	}
	// Unblock lifts the rejection.
	if code, _ := adminReq(t, "DELETE", base+"/api/admin/blocked/"+hashB, testAdminToken, nil); code != 200 {
		t.Fatalf("unblock: %d", code)
	}
	if blocked, _ := st.IsBlocked(hashB); blocked {
		t.Fatal("still blocked after unblock")
	}
	if code, _ := adminReq(t, "DELETE", base+"/api/admin/blocked/"+hashB, testAdminToken, nil); code != http.StatusNotFound {
		t.Fatalf("second unblock: %d", code)
	}

	// Delete removes without blocking.
	if code, _ := adminReq(t, "DELETE", base+"/api/admin/torrents/"+hashC, testAdminToken, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if blocked, _ := st.IsBlocked(hashC); blocked {
		t.Fatal("delete must not block")
	}
	if n, _ := st.Count(t.Context()); n != 1 {
		t.Fatalf("count after delete: %d", n)
	}
	if code, _ := adminReq(t, "DELETE", base+"/api/admin/torrents/"+hashC, testAdminToken, nil); code != http.StatusNotFound {
		t.Fatalf("second delete: %d", code)
	}
}

func TestAdminBatch(t *testing.T) {
	base, _ := adminFixture(t, testAdminToken)
	hashes := []string{strings.Repeat("a", 40), strings.Repeat("b", 40)}

	code, m := adminReq(t, "POST", base+"/api/admin/torrents/batch", testAdminToken,
		map[string]any{"hashes": hashes, "action": "approve"})
	if code != 200 || m["affected"].(float64) != 2 {
		t.Fatalf("batch approve: %d %v", code, m)
	}
	_, m = adminReq(t, "GET", base+"/api/admin/overview", testAdminToken, nil)
	if m["pending_review"].(float64) != 1 {
		t.Fatalf("pending after batch approve: %v", m)
	}

	code, m = adminReq(t, "POST", base+"/api/admin/torrents/batch", testAdminToken,
		map[string]any{"hashes": hashes, "action": "block"})
	if code != 200 || m["affected"].(float64) != 2 {
		t.Fatalf("batch block: %d %v", code, m)
	}
	_, m = adminReq(t, "GET", base+"/api/admin/overview", testAdminToken, nil)
	if m["blocked"].(float64) != 2 || m["torrents"].(float64) != 1 {
		t.Fatalf("overview after batch block: %v", m)
	}

	if code, _ := adminReq(t, "POST", base+"/api/admin/torrents/batch", testAdminToken,
		map[string]any{"hashes": hashes, "action": "explode"}); code != http.StatusBadRequest {
		t.Fatalf("bad batch action: %d", code)
	}
	// Batch without auth is refused.
	if code, _ := adminReq(t, "POST", base+"/api/admin/torrents/batch", "",
		map[string]any{"hashes": hashes, "action": "approve"}); code != http.StatusUnauthorized {
		t.Fatalf("batch without token: %d", code)
	}
}
