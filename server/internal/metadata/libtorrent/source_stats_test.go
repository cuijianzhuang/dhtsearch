package libtorrent

import (
	"context"
	"testing"
	"time"

	"dhtsearch/server/internal/metadata"
)

// TestSourceAttributionFetched: with the fake sidecar answering ok,
// one item per source through the channel path attributes its
// success to its own row — the libtorrent foundation attributes
// identically to the other two, even though the fetch itself
// happens in the sidecar process.
func TestSourceAttributionFetched(t *testing.T) {
	requireFake(t)
	t.Setenv("FAKE_SIDECAR_MODE", "ok")
	f, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan metadata.Item, 3)
	records := make(chan metadata.Record, 3)
	f.Run(ctx, in, func(r metadata.Record) { records <- r })
	in <- metadata.Item{Hash: testHash, Source: metadata.SourceDHT}
	in <- metadata.Item{Hash: "1111111111111111111111111111111111111111", Source: metadata.SourceScrape}
	in <- metadata.Item{Hash: "2222222222222222222222222222222222222222", Source: metadata.SourceFeed}
	for i := 0; i < 3; i++ {
		select {
		case <-records:
		case <-time.After(5 * time.Second):
			t.Fatal("missing record")
		}
	}
	stats := f.SourceStats()
	for _, row := range stats {
		if row.Attempts != 1 || row.Fetched != 1 {
			t.Fatalf("row = %+v, want attempts 1 fetched 1 (all rows: %+v)", row, stats)
		}
	}
	if got, _, _ := f.Stats(); got != 3 {
		t.Fatalf("global fetched = %d, want 3", got)
	}
}

// TestSourceAttributionPrescreen: with the fake sidecar never
// reporting a peer, queue items die in prescreen under their own
// tags.
func TestSourceAttributionPrescreen(t *testing.T) {
	requireFake(t)
	t.Setenv("FAKE_SIDECAR_MODE", "nopeer")
	cfg := testConfig()
	cfg.Timeout = 30 * time.Second
	cfg.PrescreenTimeout = 150 * time.Millisecond
	f, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := metadata.NewQueue(0)
	f.RunQueue(ctx, q, func(r metadata.Record) { t.Errorf("unexpected record %+v", r) })
	q.Push(metadata.Item{Hash: testHash, Source: metadata.SourceScrape})
	q.Push(metadata.Item{Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Source: metadata.SourceFeed})

	waitFor(t, "per-source prescreen drops", func() bool {
		stats := f.SourceStats()
		return stats[1].PrescreenDropped == 1 && stats[2].PrescreenDropped == 1
	})
	stats := f.SourceStats()
	if stats[0].Attempts != 0 || stats[1].Attempts != 1 || stats[2].Attempts != 1 {
		t.Fatalf("attempts by row = %+v, want dht 0 scrape 1 feed 1", stats)
	}
	if got := f.PrescreenDropped(); got != 2 {
		t.Fatalf("global prescreen_dropped = %d, want 2", got)
	}
}
