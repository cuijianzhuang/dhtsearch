package libtorrent_test

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/metadata/libtorrent"
)

// Loopback end-to-end against the REAL sidecar binary: an anacrolix
// client seeds a torrent's metadata, a toy HTTP tracker introduces
// the two, and the libtorrent fetcher must come back with the
// Record. Skipped unless DHTSEARCH_LT_SIDECAR points at a built
// dhtsearch-lt-sidecar — CI runs the equivalent protocol smoke
// against the compiled binary, and this test is for hosts where a
// real exchange can happen (the dev sandbox blocks UDP, but the
// whole path here is loopback TCP/HTTP).
func TestRealSidecarLoopbackExchange(t *testing.T) {
	sidecar := os.Getenv("DHTSEARCH_LT_SIDECAR")
	if sidecar == "" {
		t.Skip("DHTSEARCH_LT_SIDECAR not set; skipping real-sidecar integration test")
	}

	tracker := newToyTracker(t)

	// Seeder: an anacrolix client holding the info dict, announcing
	// to the toy tracker.
	cfg := torrent.NewDefaultClientConfig()
	cfg.Debug = os.Getenv("LT_TEST_DEBUG_SEEDER") != ""
	cfg.NoDHT = true
	cfg.DisableUTP = true
	cfg.DisableIPv6 = true
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cfg.DataDir = t.TempDir()
	seeder, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer seeder.Close()

	info := metainfo.Info{
		Name:        "lt-sidecar-loopback",
		PieceLength: 16384,
		Length:      12345,
		Pieces:      make([]byte, 20),
	}
	raw, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	hash := metainfo.HashBytes(raw)
	seedTor, _, err := seeder.AddTorrentSpec(&torrent.TorrentSpec{
		Trackers: [][]string{{tracker.url()}},
		AddTorrentOpts: torrent.AddTorrentOpts{
			InfoHash: hash, InfoBytes: raw,
			DisallowDataUpload: true,
			// NOT DisableInitialPieceCheck: with the check
			// disabled anacrolix treats the torrent as
			// complete-but-unverified, which makes it reject
			// every incoming connection ("don't want conns").
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Keep the seeder willing to accept connections despite its
	// empty storage (same trick as the anacrolix fetcher's own
	// loopback test); without this it resets peers after handshake.
	seedTor.DownloadAll()

	// The seeder announces asynchronously; the sidecar can only be
	// introduced to it once the tracker knows it. (In production the
	// swarm long predates the fetch; here we have to wait for it.)
	ihKey := string(hash[:])
	regDeadline := time.Now().Add(20 * time.Second)
	for tracker.peerCount(ihKey) == 0 {
		if time.Now().After(regDeadline) {
			t.Fatalf("seeder never announced to the tracker; listen addrs: %v", seeder.ListenAddrs())
		}
		time.Sleep(100 * time.Millisecond)
	}

	f, err := libtorrent.New(libtorrent.Config{
		Config: metadata.Config{
			Workers:          2,
			Timeout:          30 * time.Second,
			PrescreenEnabled: true,
			PrescreenTimeout: 10 * time.Second,
			Trackers:         []string{tracker.url()},
			Logger:           log.New(os.Stderr, "lt-test ", 0),
		},
		SidecarPath: sidecar,
	})
	if err != nil {
		t.Fatalf("New with real sidecar: %v", err)
	}
	defer f.Close()

	in := make(chan metadata.Item, 1)
	in <- metadata.Item{Hash: hash.HexString()}
	close(in)
	// Run is fire-and-forget (workers in the background, like the
	// other foundations); poll for the record.
	var mu sync.Mutex
	var got *metadata.Record
	f.Run(context.Background(), in, func(r metadata.Record) {
		mu.Lock()
		rec := r
		got = &rec
		mu.Unlock()
	})
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := got != nil
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if got == nil {
		fetched, timedOut, failed := f.Stats()
		t.Fatalf("no record; stats fetched=%d timedOut=%d failed=%d prescreen=%d",
			fetched, timedOut, failed, f.PrescreenDropped())
	}
	if got.Name != info.Name || got.TotalSize != info.Length || got.FileCount != 1 {
		t.Fatalf("record = %+v, want name=%q size=%d files=1", *got, info.Name, info.Length)
	}
	if len(got.Files) != 1 || got.Files[0].Path != info.Name || got.Files[0].Size != info.Length {
		t.Fatalf("record files = %+v", got.Files)
	}
	if fetched, _, _ := f.Stats(); fetched != 1 {
		t.Fatalf("fetched = %d, want 1", fetched)
	}
}

// seedLoopback starts an anacrolix client seeding one torrent's
// metadata on loopback TCP and returns the client, the torrent's
// infohash and its info. With trackers nil the seeder announces
// nowhere: the only way to reach it is to already know its address.
func seedLoopback(t *testing.T, trackers []string) (*torrent.Client, metainfo.Hash, metainfo.Info) {
	t.Helper()
	cfg := torrent.NewDefaultClientConfig()
	cfg.NoDHT = true
	cfg.DisableUTP = true
	cfg.DisableIPv6 = true
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cfg.DataDir = t.TempDir()
	seeder, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { seeder.Close() })

	info := metainfo.Info{
		Name:        "lt-sidecar-loopback",
		PieceLength: 16384,
		Length:      12345,
		Pieces:      make([]byte, 20),
	}
	raw, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	hash := metainfo.HashBytes(raw)
	spec := &torrent.TorrentSpec{
		AddTorrentOpts: torrent.AddTorrentOpts{
			InfoHash:           hash,
			InfoBytes:          raw,
			DisallowDataUpload: true,
		},
	}
	if trackers != nil {
		spec.Trackers = [][]string{trackers}
	}
	seedTor, _, err := seeder.AddTorrentSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	seedTor.DownloadAll()
	return seeder, hash, info
}

func runQueueOne(t *testing.T, f *libtorrent.Fetcher, it metadata.Item) *metadata.Record {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := metadata.NewQueue(0)
	q.Push(it)
	var mu sync.Mutex
	var got *metadata.Record
	f.RunQueue(ctx, q, func(r metadata.Record) {
		mu.Lock()
		rec := r
		got = &rec
		mu.Unlock()
	})
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := got != nil
		mu.Unlock()
		if done {
			return got
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// Hint-only exchange: no tracker anywhere — the seeder announces
// nowhere and the fetcher's magnet carries no &tr=. The ONLY peer
// supply is the queue item's peer hint (metadata.Item.Peers, the
// #46 cache contents), forwarded to the sidecar as the fetch
// frame's peer_hints. Proves the hint path end to end against the
// real sidecar.
func TestRealSidecarHintOnlyExchange(t *testing.T) {
	sidecar := os.Getenv("DHTSEARCH_LT_SIDECAR")
	if sidecar == "" {
		t.Skip("DHTSEARCH_LT_SIDECAR not set; skipping real-sidecar integration test")
	}
	seeder, hash, info := seedLoopback(t, nil)

	var hint netip.AddrPort
	for _, a := range seeder.ListenAddrs() {
		if ap, err := netip.ParseAddrPort(a.String()); err == nil && ap.Addr().IsLoopback() {
			hint = ap
			break
		}
	}
	if !hint.IsValid() {
		t.Fatalf("seeder has no loopback listen addr: %v", seeder.ListenAddrs())
	}

	f, err := libtorrent.New(libtorrent.Config{
		Config: metadata.Config{
			Workers:          2,
			Timeout:          30 * time.Second,
			PrescreenEnabled: true,
			PrescreenTimeout: 10 * time.Second,
			Trackers:         nil, // no trackers: the magnet is bare
			Logger:           log.New(os.Stderr, "lt-test ", 0),
		},
		SidecarPath: sidecar,
	})
	if err != nil {
		t.Fatalf("New with real sidecar: %v", err)
	}
	defer f.Close()

	got := runQueueOne(t, f, metadata.Item{
		Hash: hash.HexString(), Seeders: -1, Source: metadata.SourceDHT,
		At: time.Now(), Peers: []netip.AddrPort{hint},
	})
	if got == nil {
		fetched, timedOut, failed := f.Stats()
		t.Fatalf("no record via hint-only fetch; stats fetched=%d timedOut=%d failed=%d prescreen=%d",
			fetched, timedOut, failed, f.PrescreenDropped())
	}
	if got.Name != info.Name || got.TotalSize != info.Length || got.FileCount != 1 {
		t.Fatalf("record = %+v, want name=%q size=%d files=1", *got, info.Name, info.Length)
	}
}

// DHT status against the real sidecar: the session's DHT must
// report itself running through Fetcher.DHTStatus. The routing
// table's node count is NOT asserted: this sandbox blocks UDP
// (even loopback sendto is EPERM), so the table cannot populate
// here — in production the count growing is the visible proof.
// The trackerless, hintless fetch of an unknown hash must still
// classify normally (prescreen drop / timeout), never wedge.
func TestRealSidecarDHTStatus(t *testing.T) {
	sidecar := os.Getenv("DHTSEARCH_LT_SIDECAR")
	if sidecar == "" {
		t.Skip("DHTSEARCH_LT_SIDECAR not set; skipping real-sidecar integration test")
	}
	f, err := libtorrent.New(libtorrent.Config{
		Config: metadata.Config{
			Workers:          1,
			Timeout:          8 * time.Second,
			PrescreenEnabled: true,
			PrescreenTimeout: 3 * time.Second,
			Logger:           log.New(os.Stderr, "lt-test ", 0),
		},
		SidecarPath: sidecar,
	})
	if err != nil {
		t.Fatalf("New with real sidecar: %v", err)
	}
	defer f.Close()

	deadline := time.Now().Add(20 * time.Second)
	for {
		running, nodes := f.DHTStatus()
		if running {
			t.Logf("sidecar DHT running, nodes=%d (0 expected where UDP is blocked)", nodes)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sidecar DHT never reported running; last status running=%v nodes=%d", running, nodes)
		}
		time.Sleep(100 * time.Millisecond)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan metadata.Item, 1)
	in <- metadata.Item{Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	f.Run(ctx, in, func(r metadata.Record) { t.Errorf("unexpected record %+v", r) })
	waitDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(waitDeadline) {
		if f.PrescreenDropped() > 0 {
			return
		}
		_, timedOut, _ := f.Stats()
		if timedOut > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	fetched, timedOut, failed := f.Stats()
	t.Fatalf("unknown-hash fetch never classified; fetched=%d timedOut=%d failed=%d prescreen=%d",
		fetched, timedOut, failed, f.PrescreenDropped())
}

// toyTracker is the smallest BEP 3 HTTP tracker that can introduce
// two loopback clients: announces register (infohash, port), and
// the response carries every other registered peer in compact form.
type toyTracker struct {
	mu    sync.Mutex
	peers map[string]map[int]struct{} // infohash -> ports
	ln    net.Listener
	t     *testing.T
}

func (tt *toyTracker) peerCount(ih string) int {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return len(tt.peers[ih])
}

func newToyTracker(t *testing.T) *toyTracker {
	t.Helper()
	tt := &toyTracker{peers: make(map[string]map[int]struct{}), t: t}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tt.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/announce", tt.announce)
	go func() { _ = http.Serve(ln, mux) }()
	t.Cleanup(func() { ln.Close() })
	return tt
}

func (tt *toyTracker) url() string {
	return fmt.Sprintf("http://%s/announce", tt.ln.Addr().String())
}

func (tt *toyTracker) announce(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ih := q.Get("info_hash")
	var port int
	fmt.Sscanf(q.Get("port"), "%d", &port)

	tt.mu.Lock()
	if tt.peers[ih] == nil {
		tt.peers[ih] = make(map[int]struct{})
	}
	if port > 0 {
		tt.peers[ih][port] = struct{}{}
	}
	var compact []byte
	for p := range tt.peers[ih] {
		if p == port {
			continue
		}
		compact = append(compact, 127, 0, 0, 1, byte(p>>8), byte(p))
	}
	tt.mu.Unlock()
	tt.t.Logf("tracker: announce port=%d peers_returned=%d", port, len(compact)/6)

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "d8:completei1e10:incompletei0e8:intervali30e5:peers%d:", len(compact))
	w.Write(compact)
	fmt.Fprint(w, "e")
}
