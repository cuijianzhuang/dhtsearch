package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/nodepush"
	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
)

const testNodeToken = "node-secret-0123456789"

// nodeFixture serves the API over an in-memory store with the node
// surface configured by env: the center-side switch and token come
// from the same registry the production wiring seeds.
func nodeFixture(t *testing.T, env map[string]string) (string, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mgr, err := settings.New(env, map[string]bool{}, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, nil, nil, Options{Settings: mgr}).Handler())
	t.Cleanup(srv.Close)
	return srv.URL, st
}

func nodeEnabledEnv() map[string]string {
	return map[string]string{
		settings.KeyNodeIngestEnabled: "true",
		settings.KeyNodeToken:         testNodeToken,
	}
}

// nodePost issues one node-surface POST and returns the status and
// decoded body. An empty token sends no Authorization header.
func nodePost(t *testing.T, url, path, token string, body any) (int, map[string]any) {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url+path, bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
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

func TestNodeSurfaceDisabledByDefault(t *testing.T) {
	// No env at all: NODE_INGEST_ENABLED defaults off, so both
	// endpoints answer 503 — even with a token presented.
	url, _ := nodeFixture(t, map[string]string{})
	for _, path := range []string{"/api/node/check", "/api/node/ingest"} {
		code, _ := nodePost(t, url, path, testNodeToken, map[string]any{})
		if code != http.StatusServiceUnavailable {
			t.Fatalf("%s with ingest disabled: status %d, want 503", path, code)
		}
	}
}

func TestNodeSurfaceAuth(t *testing.T) {
	url, _ := nodeFixture(t, nodeEnabledEnv())
	// No token and wrong token are both 401; the right one passes.
	for _, token := range []string{"", "wrong-token"} {
		code, _ := nodePost(t, url, "/api/node/check", token, nodepush.CheckRequest{})
		if code != http.StatusUnauthorized {
			t.Fatalf("check with token %q: status %d, want 401", token, code)
		}
	}
	code, _ := nodePost(t, url, "/api/node/check", testNodeToken, nodepush.CheckRequest{})
	if code != http.StatusOK {
		t.Fatalf("check with the right token: status %d, want 200", code)
	}

	// Enabled but with NO token configured: nobody authenticates —
	// an empty configured secret must never match, not even an
	// empty presented one.
	url2, _ := nodeFixture(t, map[string]string{settings.KeyNodeIngestEnabled: "true"})
	code, _ = nodePost(t, url2, "/api/node/check", "", nodepush.CheckRequest{})
	if code != http.StatusUnauthorized {
		t.Fatalf("check with unconfigured center token: status %d, want 401", code)
	}
}

func TestNodeCheckReportsStoredAndBackoff(t *testing.T) {
	url, st := nodeFixture(t, nodeEnabledEnv())
	ctx := context.Background()
	storedHash := strings.Repeat("a", 40)
	if err := st.Upsert(store.Torrent{
		InfoHash: storedHash, Name: "Already Here", TotalSize: 1 << 30,
		FileCount: 1, Files: []filter.File{{Path: "a.mkv", Size: 1 << 30}}, CreatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	backoffHash := strings.Repeat("b", 40)
	if _, err := st.RecordFetchFailure(ctx, backoffHash, "timeout", time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	unknownHash := strings.Repeat("c", 40)

	code, body := nodePost(t, url, "/api/node/check", testNodeToken, nodepush.CheckRequest{
		Hashes: []string{storedHash, backoffHash, unknownHash, "not-a-hash"},
	})
	if code != http.StatusOK {
		t.Fatalf("check: status %d, want 200 (%v)", code, body)
	}
	if got := body["stored"]; !reflect.DeepEqual(got, []any{storedHash}) {
		t.Fatalf("stored = %v, want [%s]", got, storedHash)
	}
	if got := body["backoff"]; !reflect.DeepEqual(got, []any{backoffHash}) {
		t.Fatalf("backoff = %v, want [%s]", got, backoffHash)
	}

	// Oversized check calls are refused.
	big := make([]string, nodeCheckMaxHashes+1)
	for i := range big {
		big[i] = unknownHash
	}
	code, _ = nodePost(t, url, "/api/node/check", testNodeToken, nodepush.CheckRequest{Hashes: big})
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized check: status %d, want 413", code)
	}
}

func TestNodeIngestEndToEnd(t *testing.T) {
	url, st := nodeFixture(t, nodeEnabledEnv())
	ctx := context.Background()
	filter.SetBlockWords([]string{"nodeblockword"})
	t.Cleanup(func() { filter.SetBlockWords(nil) })

	goodHash := strings.Repeat("d", 40)
	blockedHash := strings.Repeat("e", 40)
	// The good hash carries an unexpired failure backoff at the
	// center (an earlier worker failed it); a successful ingest
	// must retire that row.
	if _, err := st.RecordFetchFailure(ctx, goodHash, "timeout", time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}

	files := []filter.File{{Path: "movie.mkv", Size: 2 << 30}, {Path: "subs/en.srt", Size: 12345}}
	code, body := nodePost(t, url, "/api/node/ingest", testNodeToken, nodepush.IngestRequest{
		Node: "Worker One!",
		Records: []nodepush.RecordWire{
			{InfoHash: goodHash, Name: "A Fine Film", TotalSize: 2<<30 + 12345, FileCount: 2, Files: files, Source: "scrape", Alias: "feed-alias"},
			{InfoHash: blockedHash, Name: "nodeblockword special", TotalSize: 2 << 30, FileCount: 1, Files: []filter.File{{Path: "x.mkv", Size: 2 << 30}}, Source: "dht"},
		},
		Swarm: []nodepush.SwarmWire{
			{InfoHash: goodHash, Seeders: 7, Leechers: 3, Completed: 11},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("ingest: status %d, want 200 (%v)", code, body)
	}
	if body["ingested"] != float64(1) || body["filtered"] != float64(1) || body["swarm"] != float64(1) {
		t.Fatalf("ingest summary = %v, want ingested=1 filtered=1 swarm=1", body)
	}

	// The good record landed byte-identical, files blob included,
	// with the worker's alias attached; the blocked one did not.
	items, total, err := st.Search(ctx, "A Fine Film", 1, 10)
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("search stored record: total=%d items=%v err=%v", total, items, err)
	}
	got := items[0]
	if got.InfoHash != goodHash || got.Name != "A Fine Film" || got.TotalSize != 2<<30+12345 || got.FileCount != 2 {
		t.Fatalf("stored record = %+v", got)
	}
	if !reflect.DeepEqual(got.Files, files) {
		t.Fatalf("stored files = %+v, want %+v", got.Files, files)
	}
	if got.Alias != "feed-alias" {
		t.Fatalf("stored alias = %q, want feed-alias", got.Alias)
	}
	if _, total, _ := st.Search(ctx, "nodeblockword", 1, 10); total != 0 {
		t.Fatalf("blocked record was stored (total=%d)", total)
	}

	// The negative-cache row for the ingested hash is gone.
	if _, found, err := st.FetchAttemptRow(ctx, goodHash); err != nil || found {
		t.Fatalf("fetch attempt after ingest: found=%v err=%v, want cleared", found, err)
	}

	// Swarm merged through the store's swarm write.
	swarm, err := st.SwarmStatsFor(ctx, []string{goodHash})
	if err != nil {
		t.Fatal(err)
	}
	sw, ok := swarm[goodHash]
	if !ok || sw.Seeders != 7 || sw.Leechers != 3 || sw.Completed != 11 {
		t.Fatalf("swarm = %+v (ok=%v), want 7/3/11", sw, ok)
	}

	// Counters: the global fetched, the per-node key (sanitized
	// node name), the per-source rows for the scrape record, and
	// the blocked drop.
	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int64{
		"fetched":                 1,
		"node_ingest_worker_one_": 1,
		"fetch_scrape_attempts":   1,
		"fetch_scrape_fetched":    1,
		"fetch_dht_attempts":      1,
		"fetch_dht_fetched":       1,
		"blocked_filtered":        1,
	} {
		if stats[key] != want {
			t.Errorf("stats[%q] = %d, want %d", key, stats[key], want)
		}
	}
}

func TestNodeIngestOversizedBatch(t *testing.T) {
	url, _ := nodeFixture(t, nodeEnabledEnv())
	recs := make([]nodepush.RecordWire, nodeIngestMaxRecords+1)
	for i := range recs {
		recs[i] = nodepush.RecordWire{InfoHash: strings.Repeat("f", 40), Name: "x", TotalSize: 1 << 30}
	}
	code, _ := nodePost(t, url, "/api/node/ingest", testNodeToken, nodepush.IngestRequest{Node: "w", Records: recs})
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized ingest: status %d, want 413", code)
	}
}

func TestNodeIngestStatKey(t *testing.T) {
	for name, want := range map[string]string{
		"Worker One!":           "node_ingest_worker_one_",
		"host-1_eu":             "node_ingest_host-1_eu",
		"":                      "node_ingest_unknown",
		"  ":                    "node_ingest_unknown",
		strings.Repeat("a", 40): "node_ingest_" + strings.Repeat("a", 32),
	} {
		if got := nodeIngestStatKey(name); got != want {
			t.Errorf("nodeIngestStatKey(%q) = %q, want %q", name, got, want)
		}
	}
}
