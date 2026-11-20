package metadata

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// fakeInfoSource scripts the two signals waitForInfo consumes.
type fakeInfoSource struct {
	infoCh chan struct{}
	mu     sync.Mutex
	peers  int
}

func newFakeInfoSource() *fakeInfoSource {
	return &fakeInfoSource{infoCh: make(chan struct{})}
}

func (s *fakeInfoSource) GotInfo() <-chan struct{} { return s.infoCh }
func (s *fakeInfoSource) ActivePeers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peers
}
func (s *fakeInfoSource) setPeers(n int) {
	s.mu.Lock()
	s.peers = n
	s.mu.Unlock()
}
func (s *fakeInfoSource) deliver() { close(s.infoCh) }

func TestWaitForInfoImmediate(t *testing.T) {
	src := newFakeInfoSource()
	src.deliver()
	if got := waitForInfo(context.Background(), src, 5*time.Second, time.Second); got != waitGotInfo {
		t.Fatalf("outcome = %v, want waitGotInfo", got)
	}
}

func TestWaitForInfoPrescreenDrop(t *testing.T) {
	src := newFakeInfoSource() // never connects, never delivers
	start := time.Now()
	got := waitForInfo(context.Background(), src, 5*time.Second, 300*time.Millisecond)
	elapsed := time.Since(start)
	if got != waitPrescreened {
		t.Fatalf("outcome = %v, want waitPrescreened", got)
	}
	if elapsed < 300*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("prescreen drop after %v, want ~300ms", elapsed)
	}
}

func TestWaitForInfoPrescreenPassThenInfo(t *testing.T) {
	src := newFakeInfoSource()
	go func() {
		time.Sleep(100 * time.Millisecond)
		src.setPeers(1)
		time.Sleep(400 * time.Millisecond)
		src.deliver()
	}()
	if got := waitForInfo(context.Background(), src, 5*time.Second, 300*time.Millisecond); got != waitGotInfo {
		t.Fatalf("outcome = %v, want waitGotInfo", got)
	}
}

// A peer that connects inside the window keeps the FULL remaining
// budget: prescreen must not shrink the wait for live swarms.
func TestWaitForInfoPrescreenPassThenFullTimeout(t *testing.T) {
	src := newFakeInfoSource()
	go func() {
		time.Sleep(100 * time.Millisecond)
		src.setPeers(1)
	}()
	start := time.Now()
	got := waitForInfo(context.Background(), src, 900*time.Millisecond, 300*time.Millisecond)
	elapsed := time.Since(start)
	if got != waitTimedOut {
		t.Fatalf("outcome = %v, want waitTimedOut", got)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("gave up after %v, want the full 900ms budget", elapsed)
	}
}

func TestWaitForInfoNoPrescreen(t *testing.T) {
	src := newFakeInfoSource()
	start := time.Now()
	got := waitForInfo(context.Background(), src, 400*time.Millisecond, 0)
	if got != waitTimedOut {
		t.Fatalf("outcome = %v, want waitTimedOut", got)
	}
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Fatalf("gave up after %v, want ~400ms", elapsed)
	}
}

func TestWaitForInfoCtxCancel(t *testing.T) {
	src := newFakeInfoSource()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if got := waitForInfo(ctx, src, 5*time.Second, time.Second); got != waitAborted {
		t.Fatalf("outcome = %v, want waitAborted", got)
	}
}

// blockingInfoSource's ActivePeers never returns: it stands in for a
// Torrent.Stats call parked on the torrent client's wedged lock (in
// anacrolix/torrent v1.61, Torrent.Drop waits on a WaitGroup while
// holding the client write lock, so one stuck Drop parks every later
// Stats/Add call). waitForInfo must keep its own exits regardless —
// before the peer sampler moved to a helper goroutine, each of these
// waits hung forever.
type blockingInfoSource struct {
	infoCh chan struct{}
}

func (s *blockingInfoSource) GotInfo() <-chan struct{} { return s.infoCh }
func (s *blockingInfoSource) ActivePeers() int         { select {} }

func TestWaitForInfoBlockingActivePeers(t *testing.T) {
	t.Run("prescreen still expires", func(t *testing.T) {
		src := &blockingInfoSource{infoCh: make(chan struct{})}
		start := time.Now()
		got := waitForInfo(context.Background(), src, 5*time.Second, 300*time.Millisecond)
		if got != waitPrescreened {
			t.Fatalf("outcome = %v, want waitPrescreened", got)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("prescreen took %v with a parked ActivePeers, want ~300ms", elapsed)
		}
	})
	t.Run("ctx cancel still aborts", func(t *testing.T) {
		src := &blockingInfoSource{infoCh: make(chan struct{})}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if got := waitForInfo(ctx, src, 5*time.Second, time.Second); got != waitAborted {
			t.Fatalf("outcome = %v, want waitAborted", got)
		}
	})
	t.Run("info still lands", func(t *testing.T) {
		src := &blockingInfoSource{infoCh: make(chan struct{})}
		go func() {
			time.Sleep(100 * time.Millisecond)
			close(src.infoCh)
		}()
		if got := waitForInfo(context.Background(), src, 5*time.Second, time.Second); got != waitGotInfo {
			t.Fatalf("outcome = %v, want waitGotInfo", got)
		}
	})
}

// loopbackClient builds a torrent client confined to 127.0.0.1 TCP,
// the same shape the fetcher's own tests use.
func loopbackClient(t *testing.T) *torrent.Client {
	t.Helper()
	cfg := metadataClientConfig()
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.DisableUTP = true
	cfg.DisableIPv6 = true
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

// TestPrescreenAB runs the same mixed batch — dead hashes nobody
// serves, plus live torrents a local peer does serve — through the
// real fetcher with prescreen off and on. It is the A/B the feature
// stands on: prescreen must release dead hashes after the window
// instead of the full timeout, without losing a single live fetch.
func TestPrescreenAB(t *testing.T) {
	// One shared source client serving both live torrents.
	source := loopbackClient(t)
	liveInfos := []metainfo.Info{
		{Name: "Live One " + strings.Repeat("a", 40), PieceLength: 16384, Length: 1, Pieces: make([]byte, 20)},
		{Name: "Live Two " + strings.Repeat("b", 40), PieceLength: 16384, Length: 1, Pieces: make([]byte, 20)},
	}
	var liveHashes []string
	for _, info := range liveInfos {
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
		st.DownloadAll() // keep the source willing to accept connections
		liveHashes = append(liveHashes, hash.HexString())
	}
	var deadHashes []string
	for i := 0; i < 6; i++ {
		deadHashes = append(deadHashes, metainfo.HashBytes([]byte("dead-hash-"+strings.Repeat("d", i+1))).HexString())
	}

	// makeFetcher builds a fetcher over a fresh client with the live
	// torrents pre-added and peered to the source, then waits until
	// the peer connections are actually up — so the batch measures the
	// fetch waits, not loopback handshake jitter.
	makeFetcher := func(cfg Config) *Fetcher {
		dest := loopbackClient(t)
		for _, lh := range liveHashes {
			var hash metainfo.Hash
			if err := hash.FromHexString(lh); err != nil {
				t.Fatal(err)
			}
			tor, _, err := dest.AddTorrentSpec(&torrent.TorrentSpec{AddTorrentOpts: torrent.AddTorrentOpts{
				InfoHash: hash, DisallowDataDownload: true, DisallowDataUpload: true, DisableInitialPieceCheck: true,
			}})
			if err != nil {
				t.Fatal(err)
			}
			tor.AddPeers([]torrent.PeerInfo{{Addr: source.ListenAddrs()[0], Trusted: true}})
			deadline := time.Now().Add(10 * time.Second)
			for tor.Stats().ActivePeers == 0 && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if tor.Stats().ActivePeers == 0 {
				t.Fatalf("live torrent %s never connected to source", lh)
			}
		}
		cfg.MaxFiles = 50
		return &Fetcher{client: dest, cfg: cfg}
	}

	runBatch := func(f *Fetcher, hashes []string, workers int) (fetched int, wall time.Duration) {
		var mu sync.Mutex
		var wg sync.WaitGroup
		jobs := make(chan string)
		start := time.Now()
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for h := range jobs {
					if _, ok := f.fetch(context.Background(), h); ok {
						mu.Lock()
						fetched++
						mu.Unlock()
					}
				}
			}()
		}
		for _, h := range hashes {
			jobs <- h
		}
		close(jobs)
		wg.Wait()
		return fetched, time.Since(start)
	}

	batch := append(append([]string{}, deadHashes...), liveHashes...)

	off := makeFetcher(Config{Timeout: 2 * time.Second})
	offFetched, offWall := runBatch(off, batch, 3)
	_, offTimedOut, _ := off.Stats()

	on := makeFetcher(Config{Timeout: 2 * time.Second, PrescreenEnabled: true, PrescreenTimeout: 800 * time.Millisecond})
	onFetched, onWall := runBatch(on, batch, 3)
	_, onTimedOut, _ := on.Stats()

	t.Logf("prescreen OFF: fetched=%d timed_out=%d wall=%v", offFetched, offTimedOut, offWall)
	t.Logf("prescreen ON:  fetched=%d prescreen_dropped=%d timed_out=%d wall=%v",
		onFetched, on.PrescreenDropped(), onTimedOut, onWall)

	if offFetched != len(liveHashes) {
		t.Fatalf("off: fetched %d live torrents, want %d", offFetched, len(liveHashes))
	}
	if onFetched != offFetched {
		t.Fatalf("prescreen lost live fetches: on=%d off=%d", onFetched, offFetched)
	}
	if offTimedOut != int64(len(deadHashes)) {
		t.Fatalf("off: timed_out = %d, want %d", offTimedOut, len(deadHashes))
	}
	if got := on.PrescreenDropped(); got != int64(len(deadHashes)) {
		t.Fatalf("on: prescreen_dropped = %d, want %d", got, len(deadHashes))
	}
	if onTimedOut != 0 {
		t.Fatalf("on: timed_out = %d, want 0 (dead hashes should die in prescreen)", onTimedOut)
	}
	if onWall > offWall*3/5 {
		t.Fatalf("prescreen barely helped: on wall %v vs off wall %v", onWall, offWall)
	}
}

// TestRunQueuePrescreen exercises the queue -> worker -> prescreen
// path end to end over dead hashes only (no peers exist, so every
// item must come back as a prescreen drop, quickly).
func TestRunQueuePrescreen(t *testing.T) {
	dest := loopbackClient(t)
	f := &Fetcher{client: dest, cfg: Config{
		Workers: 2, Timeout: 5 * time.Second, MaxFiles: 50,
		PrescreenEnabled: true, PrescreenTimeout: 200 * time.Millisecond,
	}}
	q := NewQueue(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.RunQueue(ctx, q, func(Record) { t.Error("dead hash produced a record") })
	for i := 0; i < 3; i++ {
		q.Push(Item{Hash: metainfo.HashBytes([]byte{byte(i), 42}).HexString(), Source: SourceDHT})
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.PrescreenDropped() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := f.PrescreenDropped(); got != 3 {
		t.Fatalf("prescreen_dropped = %d, want 3", got)
	}
}
