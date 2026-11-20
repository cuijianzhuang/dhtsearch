package main

import (
	"context"
	"io"
	"log"
	"net/netip"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dhtsearch/server/internal/crawler"
	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/metadata/libtorrent"
	"dhtsearch/server/internal/metadata/native"
	"dhtsearch/server/internal/store"
)

// TestQueueItemAttachesPeerHints covers the queue bridge: an item
// built for a hash the crawler holds peer hints for carries them
// (consuming them — the next item for the same hash gets none), and
// an item for an unknown hash carries none.
func TestQueueItemAttachesPeerHints(t *testing.T) {
	cr := &crawler.Crawler{}
	hash := "97a29535b9e2f0e46a1b09a4b0c0e4f0a1b2c3d4"
	ap := netip.MustParseAddrPort("203.0.113.7:51413")

	it := queueItem(cr, hash, -1, metadata.SourceDHT)
	if len(it.Peers) != 0 {
		t.Fatalf("item for unhinted hash carries %v", it.Peers)
	}
	if it.Hash != hash || it.Seeders != -1 || it.Source != metadata.SourceDHT {
		t.Fatalf("item fields mangled: %+v", it)
	}

	cr.AddPeerHint(hash, ap)
	it = queueItem(cr, hash, 12, metadata.SourceScrape)
	if len(it.Peers) != 1 || it.Peers[0] != ap {
		t.Fatalf("item peers = %v, want [%v]", it.Peers, ap)
	}
	if it.Seeders != 12 || it.Source != metadata.SourceScrape {
		t.Fatalf("item fields mangled: %+v", it)
	}

	it = queueItem(cr, hash, -1, metadata.SourceDHT)
	if len(it.Peers) != 0 {
		t.Fatalf("hints were not consumed by the first item: %v", it.Peers)
	}
}

func selectTestConfig() metadata.Config {
	return metadata.Config{
		Workers: 2,
		Shards:  1,
		Logger:  log.New(io.Discard, "", 0),
	}
}

// buildFakeSidecar compiles the libtorrent package's fake sidecar
// (the same fixture its own tests use) and returns its path.
func buildFakeSidecar(t *testing.T) string {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool unavailable")
	}
	bin := filepath.Join(t.TempDir(), "fakesidecar")
	src := filepath.Join("..", "..", "internal", "metadata", "libtorrent", "testdata", "fakesidecar")
	cmd := exec.Command(goTool, "build", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("fake sidecar build failed: %v\n%s", err, out)
	}
	return bin
}

// TestSelectBackendLibtorrentMissingSidecarDegrades is the
// 2026-11-20 outage replayed at the selection layer: META_FETCHER
// says libtorrent, the sidecar path points at a file that does not
// exist, and startup must still produce a working backend — the
// anacrolix fallback, with the degradation named in the warning —
// instead of the error that used to Fatalf the whole service.
func TestSelectBackendLibtorrentMissingSidecarDegrades(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-sidecar")
	sel, err := selectBackend("libtorrent", missing, selectTestConfig(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("selectBackend: %v", err)
	}
	defer sel.backend.Close()
	if sel.name != "anacrolix" {
		t.Fatalf("effective backend = %q, want anacrolix fallback", sel.name)
	}
	if _, ok := sel.backend.(*metadata.Fetcher); !ok {
		t.Fatalf("fallback backend = %T, want *metadata.Fetcher", sel.backend)
	}
	if !strings.Contains(sel.warning, "libtorrent") || !strings.Contains(sel.warning, "anacrolix") {
		t.Fatalf("warning does not name the degradation: %q", sel.warning)
	}
}

// TestSelectBackendLibtorrentWorkingSidecar: with a sidecar that
// starts fine, the effective foundation is libtorrent and there is
// no warning.
func TestSelectBackendLibtorrentWorkingSidecar(t *testing.T) {
	t.Setenv("FAKE_SIDECAR_MODE", "ok")
	sel, err := selectBackend("libtorrent", buildFakeSidecar(t), selectTestConfig(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("selectBackend: %v", err)
	}
	defer sel.backend.Close()
	if sel.name != "libtorrent" {
		t.Fatalf("effective backend = %q, want libtorrent", sel.name)
	}
	if _, ok := sel.backend.(*libtorrent.Fetcher); !ok {
		t.Fatalf("backend = %T, want *libtorrent.Fetcher", sel.backend)
	}
	if sel.warning != "" {
		t.Fatalf("warning = %q, want empty", sel.warning)
	}
}

// TestSelectBackendPlainFoundations: the untouched paths — anacrolix
// and native select themselves with no warning, and an unknown value
// falls back to anacrolix with the fallback surfaced as a warning
// (effective != requested).
func TestSelectBackendPlainFoundations(t *testing.T) {
	logger := log.New(io.Discard, "", 0)

	sel, err := selectBackend("anacrolix", "", selectTestConfig(), logger)
	if err != nil {
		t.Fatalf("anacrolix: %v", err)
	}
	if sel.name != "anacrolix" || sel.warning != "" {
		t.Fatalf("anacrolix selection = %+v", sel)
	}
	sel.backend.Close()

	sel, err = selectBackend("native", "", selectTestConfig(), logger)
	if err != nil {
		t.Fatalf("native: %v", err)
	}
	if sel.name != "native" || sel.warning != "" {
		t.Fatalf("native selection = %+v", sel)
	}
	if _, ok := sel.backend.(*native.Fetcher); !ok {
		t.Fatalf("backend = %T, want *native.Fetcher", sel.backend)
	}
	sel.backend.Close()

	sel, err = selectBackend("bogus", "", selectTestConfig(), logger)
	if err != nil {
		t.Fatalf("bogus: %v", err)
	}
	defer sel.backend.Close()
	if sel.name != "anacrolix" || !strings.Contains(sel.warning, "bogus") {
		t.Fatalf("bogus selection = %+v", sel)
	}
}

// TestFeedItemsTagsFeedSource covers the feeds bridge: every hash
// the feeds service emits becomes an item tagged with the feed
// source (the attribution the dedicated feed fetcher's outcomes
// are counted under), with the unknown-seeders marker and a
// timestamp filled in.
func TestFeedItemsTagsFeedSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan string, 2)
	out <- "97a29535b9e2f0e46a1b09a4b0c0e4f0a1b2c3d4"
	out <- "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	close(out)

	var got []metadata.Item
	for it := range feedItems(ctx, out) {
		got = append(got, it)
	}
	if len(got) != 2 {
		t.Fatalf("adapted %d items, want 2", len(got))
	}
	for _, it := range got {
		if it.Source != metadata.SourceFeed {
			t.Fatalf("item source = %v, want feed (%+v)", it.Source, it)
		}
		if it.Seeders != -1 {
			t.Fatalf("item seeders = %d, want -1 (unknown)", it.Seeders)
		}
		if it.At.IsZero() {
			t.Fatalf("item has no timestamp: %+v", it)
		}
	}
	if got[0].Hash == got[1].Hash {
		t.Fatalf("hashes mangled: %+v", got)
	}
}

// TestSourceStatMirrorRoundTrip: per-source counters applied
// through the mirror land in the stats table under the shared
// keys, a second application moves only the deltas, and a third
// (unchanged counters) moves nothing — the persistence the
// by_source breakdown reads back after a restart.
func TestSourceStatMirrorRoundTrip(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "mirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	m := newSourceStatMirror()

	stats := []metadata.FetchSourceStats{
		{Source: metadata.SourceDHT, Attempts: 10, Fetched: 3, TimedOut: 4, Failed: 1, PrescreenDropped: 2},
		{Source: metadata.SourceScrape, Attempts: 5, Fetched: 5},
		{Source: metadata.SourceFeed, Attempts: 2, Fetched: 1, Failed: 1},
	}
	m.apply(ctx, st, stats)
	got, err := st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		"fetch_dht_attempts": 10, "fetch_dht_fetched": 3, "fetch_dht_timed_out": 4,
		"fetch_dht_failed": 1, "fetch_dht_prescreen_dropped": 2,
		"fetch_scrape_attempts": 5, "fetch_scrape_fetched": 5,
		"fetch_feed_attempts": 2, "fetch_feed_fetched": 1, "fetch_feed_failed": 1,
	}
	for key, w := range want {
		if got[key] != w {
			t.Fatalf("stats[%q] = %d, want %d (all: %v)", key, got[key], w, got)
		}
	}

	// Growth applies as deltas only; unchanged rows do not move.
	stats[0].Attempts, stats[0].Fetched = 15, 6
	m.apply(ctx, st, stats)
	got, err = st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["fetch_dht_attempts"] != 15 || got["fetch_dht_fetched"] != 6 {
		t.Fatalf("after growth: attempts=%d fetched=%d, want 15/6", got["fetch_dht_attempts"], got["fetch_dht_fetched"])
	}
	if got["fetch_scrape_attempts"] != 5 || got["fetch_feed_attempts"] != 2 {
		t.Fatalf("unchanged rows moved: %v", got)
	}

	// A fresh mirror (a restarted process) re-applying the same
	// cumulative counters would double-count — which is exactly
	// why the real mirror's counters restart at zero with the
	// process: a fresh backend reports zeros, and zeros against a
	// fresh mirror apply nothing.
	fresh := newSourceStatMirror()
	zeros := []metadata.FetchSourceStats{
		{Source: metadata.SourceDHT}, {Source: metadata.SourceScrape}, {Source: metadata.SourceFeed},
	}
	fresh.apply(ctx, st, zeros)
	got, err = st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["fetch_dht_attempts"] != 15 {
		t.Fatalf("restart-shaped apply moved counters: %v", got)
	}
}
