package main

import (
	"net/netip"
	"testing"

	"dhtsearch/server/internal/crawler"
	"dhtsearch/server/internal/metadata"
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
