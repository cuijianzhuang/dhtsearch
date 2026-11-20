package native

import (
	"context"
	"testing"
	"time"

	"dhtsearch/server/internal/metadata"
)

// TestSourceAttribution pins the native foundation's per-source
// counters: a success served by a loopback peer lands on the row
// of the source the fetch was tagged with (feed here — the channel
// the native foundation previously could not attribute at all), a
// prescreen drop lands on its tag's row (scrape), and a malformed
// hash on the default (dht). The other rows stay at zero and the
// global counters keep counting the same outcomes untagged.
func TestSourceAttribution(t *testing.T) {
	// Fetched, tagged feed.
	_, raw, hash := makeInfo(t, "attributed-feed", 3)
	peer := startTestPeer(t, hash, raw)
	live := newTestFetcher(t, metadata.Config{
		Workers: 2, Timeout: 10 * time.Second, MaxFiles: 50,
	}, staticSource{addrs: []string{peer.addr()}})
	if _, ok := live.fetch(context.Background(), hash.HexString(), metadata.SourceFeed); !ok {
		t.Fatal("loopback fetch failed")
	}
	assertSourceRow(t, live, metadata.SourceFeed, metadata.FetchSourceStats{
		Source: metadata.SourceFeed, Attempts: 1, Fetched: 1,
	})
	assertSourceRow(t, live, metadata.SourceDHT, metadata.FetchSourceStats{Source: metadata.SourceDHT})

	// Prescreen-dropped, tagged scrape.
	dead := newTestFetcher(t, metadata.Config{
		Workers: 2, Timeout: 30 * time.Second, MaxFiles: 50,
		PrescreenEnabled: true, PrescreenTimeout: 300 * time.Millisecond,
	}, staticSource{addrs: []string{"127.0.0.1:1"}})
	_, _, deadHash := makeInfo(t, "attributed-scrape", 1)
	if _, ok := dead.fetch(context.Background(), deadHash.HexString(), metadata.SourceScrape); ok {
		t.Fatal("dead-peer fetch unexpectedly succeeded")
	}
	assertSourceRow(t, dead, metadata.SourceScrape, metadata.FetchSourceStats{
		Source: metadata.SourceScrape, Attempts: 1, PrescreenDropped: 1,
	})

	// Failed (malformed), tagged dht.
	bad := newTestFetcher(t, metadata.Config{
		Workers: 1, Timeout: 5 * time.Second, MaxFiles: 50,
	}, staticSource{})
	if _, ok := bad.fetch(context.Background(), "not-a-hash", metadata.SourceDHT); ok {
		t.Fatal("malformed hash fetched")
	}
	assertSourceRow(t, bad, metadata.SourceDHT, metadata.FetchSourceStats{
		Source: metadata.SourceDHT, Attempts: 1, Failed: 1,
	})
	if _, _, failed := bad.Stats(); failed != 1 {
		t.Fatalf("global failed = %d, want 1", failed)
	}
}

func assertSourceRow(t *testing.T, f *Fetcher, src metadata.Source, want metadata.FetchSourceStats) {
	t.Helper()
	for _, row := range f.SourceStats() {
		if row.Source == src {
			if row != want {
				t.Fatalf("source %v row = %+v, want %+v", src, row, want)
			}
			return
		}
	}
	t.Fatalf("no row for source %v in %+v", src, f.SourceStats())
}
