package metadata

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// TestFetchPeersHintLoopback is the proof of the peer-hint path:
// the fetcher's client has no trackers (empty magnet suffix, client
// trackers disabled) and no DHT, so a hash can only be fetched if
// the supplied hint peer serves it. The same hash without a hint —
// or with a dead one — must die in prescreen.
func TestFetchPeersHintLoopback(t *testing.T) {
	source := loopbackClient(t)
	serve := func(name string) (hashHex string, wantName string) {
		info := metainfo.Info{Name: name, PieceLength: 16384, Length: 1, Pieces: make([]byte, 20)}
		raw, err := bencode.Marshal(info)
		if err != nil {
			t.Fatal(err)
		}
		hash := metainfo.HashBytes(raw)
		st, _, err := source.AddTorrentSpec(&torrent.TorrentSpec{AddTorrentOpts: torrent.AddTorrentOpts{
			InfoHash: hash, InfoBytes: raw, DisallowDataUpload: true, DisableInitialPieceCheck: true,
		}})
		if err != nil {
			t.Fatal(err)
		}
		st.DownloadAll()
		return hash.HexString(), info.Name
	}
	hintedHash, hintedName := serve("Hinted " + strings.Repeat("h", 40))
	plainHash, _ := serve("Plain " + strings.Repeat("p", 40))

	hint, err := netip.ParseAddrPort(source.ListenAddrs()[0].String())
	if err != nil {
		t.Fatal(err)
	}

	dest := loopbackClient(t)
	f := &Fetcher{clients: []*torrent.Client{dest}, cfg: Config{
		Timeout: 3 * time.Second, MaxFiles: 50,
		PrescreenEnabled: true, PrescreenTimeout: 600 * time.Millisecond,
	}}
	ctx := context.Background()

	// With the hint: connects inside prescreen and fetches.
	rec, ok := f.fetchPeers(ctx, hintedHash, []netip.AddrPort{hint})
	if !ok {
		t.Fatal("fetch with a live hint peer failed")
	}
	if rec.Name != hintedName {
		t.Fatalf("fetched name = %q, want %q", rec.Name, hintedName)
	}
	items, fetched := f.PeerHintStats()
	if items != 1 || fetched != 1 {
		t.Fatalf("PeerHintStats = (%d, %d), want (1, 1)", items, fetched)
	}

	// Without any hint the same pipeline has no peer source at all:
	// prescreen must drop it, and the hint counters must not move.
	if _, ok := f.fetchPeers(ctx, plainHash, nil); ok {
		t.Fatal("fetch without hint succeeded with no trackers and no DHT")
	}
	if got := f.PrescreenDropped(); got != 1 {
		t.Fatalf("prescreen_dropped = %d, want 1", got)
	}
	items, fetched = f.PeerHintStats()
	if items != 1 || fetched != 1 {
		t.Fatalf("PeerHintStats after unhinted fetch = (%d, %d), want (1, 1)", items, fetched)
	}

	// With a dead hint: the item counts as hinted, the fetch still
	// dies in prescreen, and the success counter must not move.
	dead := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 1)
	if _, ok := f.fetchPeers(ctx, plainHash, []netip.AddrPort{dead}); ok {
		t.Fatal("fetch with a dead hint peer succeeded")
	}
	items, fetched = f.PeerHintStats()
	if items != 2 || fetched != 1 {
		t.Fatalf("PeerHintStats after dead-hint fetch = (%d, %d), want (2, 1)", items, fetched)
	}
	if got := f.PrescreenDropped(); got != 2 {
		t.Fatalf("prescreen_dropped = %d, want 2", got)
	}
}

// TestQueueCarriesPeers: hints attached to an Item survive the
// queue round trip untouched (the bridge-to-worker hand-off).
func TestQueueCarriesPeers(t *testing.T) {
	q := NewQueue(0)
	peers := []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.7:51413"),
		netip.MustParseAddrPort("[2001:db8::7]:6881"),
	}
	if !q.Push(Item{Hash: strings.Repeat("a", 40), Seeders: -1, Source: SourceDHT, Peers: peers}) {
		t.Fatal("push refused")
	}
	it, ok := q.Pop(context.Background())
	if !ok {
		t.Fatal("pop failed")
	}
	if len(it.Peers) != len(peers) {
		t.Fatalf("popped item carries %d peers, want %d", len(it.Peers), len(peers))
	}
	for i := range peers {
		if it.Peers[i] != peers[i] {
			t.Fatalf("peer %d = %v, want %v", i, it.Peers[i], peers[i])
		}
	}
}
