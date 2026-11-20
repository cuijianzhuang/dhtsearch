package native

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"dhtsearch/server/internal/metadata"
)

// TestFetchPeersHintLoopback is the proof of the native peer-hint
// path, mirroring the incumbent's TestFetchPeersHintLoopback: the
// fetcher's peer source is empty (no tracker peers; this backend
// has no DHT by construction), so a hash can only be fetched if the
// supplied hint peer serves it. The same hash without a hint — or
// with a dead one — must die in prescreen.
func TestFetchPeersHintLoopback(t *testing.T) {
	_, rawHinted, hashHinted := makeInfo(t, "hinted", 5)
	_, _, hashPlain := makeInfo(t, "plain", 5)
	peer := startTestPeer(t, hashHinted, rawHinted)

	hint, err := netip.ParseAddrPort(peer.addr())
	if err != nil {
		t.Fatal(err)
	}

	f := newTestFetcher(t, metadata.Config{
		Workers: 2, Timeout: 10 * time.Second, MaxFiles: 50,
		PrescreenEnabled: true, PrescreenTimeout: 600 * time.Millisecond,
	}, staticSource{addrs: nil})
	ctx := context.Background()

	// With the hint: connects inside prescreen and fetches.
	rec, ok := f.fetchPeers(ctx, hashHinted.HexString(), []netip.AddrPort{hint})
	if !ok {
		t.Fatal("fetch with a live hint peer failed")
	}
	if rec.Name != "hinted" {
		t.Fatalf("fetched name = %q, want %q", rec.Name, "hinted")
	}
	items, fetched := f.PeerHintStats()
	if items != 1 || fetched != 1 {
		t.Fatalf("PeerHintStats = (%d, %d), want (1, 1)", items, fetched)
	}

	// Without any hint the pipeline has no peer source at all:
	// prescreen must drop it, and the hint counters must not move.
	if _, ok := f.fetchPeers(ctx, hashPlain.HexString(), nil); ok {
		t.Fatal("fetch without hint succeeded with an empty peer source")
	}
	if got := f.PrescreenDropped(); got != 1 {
		t.Fatalf("prescreenDropped = %d, want 1", got)
	}
	items, fetched = f.PeerHintStats()
	if items != 1 || fetched != 1 {
		t.Fatalf("PeerHintStats after unhinted fetch = (%d, %d), want (1, 1)", items, fetched)
	}

	// With a dead hint: the item counts as hinted, the fetch still
	// dies in prescreen, and the success counter must not move.
	dead := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 1)
	if _, ok := f.fetchPeers(ctx, hashPlain.HexString(), []netip.AddrPort{dead}); ok {
		t.Fatal("fetch with a dead hint peer succeeded")
	}
	items, fetched = f.PeerHintStats()
	if items != 2 || fetched != 1 {
		t.Fatalf("PeerHintStats after dead-hint fetch = (%d, %d), want (2, 1)", items, fetched)
	}
	if got := f.PrescreenDropped(); got != 2 {
		t.Fatalf("prescreenDropped = %d, want 2", got)
	}
}

// TestRunQueueCarriesHints: hints attached to a queue Item reach
// the native fetcher through RunQueue — with an empty peer source
// the record can only arrive via the hint, so delivery itself
// proves the bridge-to-worker hand-off.
func TestRunQueueCarriesHints(t *testing.T) {
	_, raw, hash := makeInfo(t, "queued-hint", 3)
	peer := startTestPeer(t, hash, raw)
	hint, err := netip.ParseAddrPort(peer.addr())
	if err != nil {
		t.Fatal(err)
	}
	f := newTestFetcher(t, metadata.Config{
		Workers: 2, Timeout: 10 * time.Second, MaxFiles: 50,
	}, staticSource{addrs: nil})

	q := metadata.NewQueue(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan metadata.Record, 1)
	f.RunQueue(ctx, q, func(rec metadata.Record) { got <- rec })
	q.Push(metadata.Item{Hash: hash.HexString(), Seeders: -1, Source: metadata.SourceDHT, Peers: []netip.AddrPort{hint}})
	select {
	case rec := <-got:
		if rec.InfoHash != hash.HexString() || rec.FileCount != 3 {
			t.Fatalf("record = %+v", rec)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no record delivered via hint")
	}
	items, fetched := f.PeerHintStats()
	if items != 1 || fetched != 1 {
		t.Fatalf("PeerHintStats = (%d, %d), want (1, 1)", items, fetched)
	}
}
