package native

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"dhtsearch/server/internal/metadata"
)

// staticSource is a peerSource that emits a fixed address list and
// returns (the announce round is instantly "complete").
type staticSource struct{ addrs []string }

func (s staticSource) Peers(ctx context.Context, ih [20]byte, out chan<- string) {
	for _, a := range s.addrs {
		select {
		case out <- a:
		case <-ctx.Done():
			return
		}
	}
}

// testPeer is a hand-rolled BEP 9 server: it speaks just enough of
// the peer protocol to serve one torrent's metadata, so the fetcher
// can be tested end to end on loopback without a torrent client. It
// advertises ut_metadata under ID 7 — not the fetcher's own ID — so
// the tests prove the fetcher addresses requests with the peer's
// advertised ID.
type testPeer struct {
	raw       []byte   // info bytes served (whatever their hash is)
	handshake [20]byte // infohash claimed in the BT handshake
	ln        net.Listener
	wg        sync.WaitGroup
}

const testPeerUtMetadataID = 7

func startTestPeer(t *testing.T, handshake [20]byte, raw []byte) *testPeer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &testPeer{raw: raw, handshake: handshake, ln: ln}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				p.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		p.wg.Wait()
	})
	return p
}

func (p *testPeer) addr() string { return p.ln.Addr().String() }

func (p *testPeer) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	remote, err := readHandshake(conn)
	if err != nil {
		return
	}
	var pid [20]byte
	copy(pid[:], "-TESTPEER-00000001")
	if _, err := conn.Write(buildHandshake(p.handshake, pid)); err != nil {
		return
	}
	_ = remote
	for {
		id, payload, err := readMessage(conn)
		if err != nil {
			return
		}
		if id != msgExtended || len(payload) == 0 {
			continue
		}
		switch payload[0] {
		case extHandshakeID:
			// Learn the ID the fetcher wants metadata data sent
			// under, then offer ours under a different ID, with
			// the size stated up front.
			theirs, err := parseExtendedHandshake(payload[1:])
			if err != nil || !theirs.hasMetadata {
				return
			}
			hs, err := bencode.Marshal(struct {
				M            map[string]int `bencode:"m"`
				MetadataSize int            `bencode:"metadata_size"`
			}{
				M:            map[string]int{"ut_metadata": testPeerUtMetadataID},
				MetadataSize: len(p.raw),
			})
			if err != nil {
				return
			}
			if err := writeMessage(conn, msgExtended, append([]byte{extHandshakeID}, hs...)); err != nil {
				return
			}
			// Reply data goes under the fetcher's advertised ID,
			// captured for the request handler below.
			p.serveRequests(conn, theirs.utMetadataID)
			return
		}
	}
}

// serveRequests answers ut_metadata requests until the connection
// ends. dataID is the extended ID the fetcher advertised for
// ut_metadata in its handshake.
func (p *testPeer) serveRequests(conn net.Conn, dataID int) {
	for {
		id, payload, err := readMessage(conn)
		if err != nil {
			return
		}
		if id != msgExtended || len(payload) == 0 || payload[0] != testPeerUtMetadataID {
			continue
		}
		hdr, _, err := splitDict(payload[1:])
		if err != nil || hdr.MsgType != metaRequest {
			continue
		}
		off := hdr.Piece * metadataPieceSize
		if off >= len(p.raw) {
			return
		}
		end := off + metadataPieceSize
		if end > len(p.raw) {
			end = len(p.raw)
		}
		head, err := bencode.Marshal(metaHeader{MsgType: metaData, Piece: hdr.Piece, TotalSize: len(p.raw)})
		if err != nil {
			return
		}
		msg := append([]byte{byte(dataID)}, head...)
		msg = append(msg, p.raw[off:end]...)
		if err := writeMessage(conn, msgExtended, msg); err != nil {
			return
		}
	}
}

// makeInfo builds a fixture info dict whose bencoded form is several
// metadata pieces long, so the tests exercise piece assembly.
func makeInfo(t *testing.T, name string, files int) (metainfo.Info, []byte, metainfo.Hash) {
	t.Helper()
	info := metainfo.Info{Name: name, PieceLength: 16384}
	if files <= 1 {
		info.Length = 12345
		info.Pieces = make([]byte, 20)
	} else {
		for i := 0; i < files; i++ {
			info.Files = append(info.Files, metainfo.FileInfo{
				Length: int64(1000 + i),
				Path:   []string{"dir", fmt.Sprintf("file-%04d.bin", i)},
			})
		}
		info.Pieces = make([]byte, 20)
	}
	raw, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	return info, raw, metainfo.HashBytes(raw)
}

func newTestFetcher(t *testing.T, cfg metadata.Config, src peerSource) *Fetcher {
	t.Helper()
	f, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if src != nil {
		f.src = src
	}
	t.Cleanup(f.Close)
	return f
}

// The headline test: metadata served by a hand-rolled peer over TCP
// comes back as the identical Record the anacrolix fetcher would
// build from the same info dict (both go through
// metadata.RecordFromInfo, fed here by the native wire path).
func TestFetchFromLoopbackPeer(t *testing.T) {
	info, raw, hash := makeInfo(t, "fixture-multi", 800)
	if len(raw) <= 2*metadataPieceSize {
		t.Fatalf("fixture too small: %d bytes, want >2 pieces", len(raw))
	}
	peer := startTestPeer(t, hash, raw)
	f := newTestFetcher(t, metadata.Config{
		Workers: 2, Timeout: 10 * time.Second, MaxFiles: 50,
	}, staticSource{addrs: []string{peer.addr()}})

	rec, ok := f.fetch(context.Background(), hash.HexString())
	if !ok {
		t.Fatal("fetch failed")
	}
	want := metadata.RecordFromInfo(&info, hash.HexString(), 50)
	if rec.Name != want.Name || rec.TotalSize != want.TotalSize || rec.FileCount != want.FileCount {
		t.Fatalf("record = %+v, want name/size/count of %+v", rec, want)
	}
	if len(rec.Files) != 50 || rec.Files[0] != want.Files[0] || rec.Files[49] != want.Files[49] {
		t.Fatalf("files mismatch: got %d entries", len(rec.Files))
	}
	if got, _, _ := f.Stats(); got != 1 {
		t.Fatalf("fetched = %d, want 1", got)
	}
}

// A peer that serves a different torrent's metadata under the
// requested hash's handshake: the assembled blob cannot hash to the
// requested infohash, so the fetch is a failure — promptly, once the
// supply is exhausted — and never a record.
func TestFetchWrongInfoRejected(t *testing.T) {
	_, rawOther, _ := makeInfo(t, "someone-else", 5)
	_, _, hashWanted := makeInfo(t, "wanted", 5)
	peer := startTestPeer(t, hashWanted, rawOther)
	f := newTestFetcher(t, metadata.Config{
		Workers: 2, Timeout: 30 * time.Second, MaxFiles: 50,
	}, staticSource{addrs: []string{peer.addr()}})

	start := time.Now()
	rec, ok := f.fetch(context.Background(), hashWanted.HexString())
	elapsed := time.Since(start)
	if ok {
		t.Fatalf("fetch accepted wrong metadata: %+v", rec)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("rejection took %v, want prompt failure once supply is exhausted", elapsed)
	}
	if _, _, failed := f.Stats(); failed != 1 {
		t.Fatalf("failed = %d, want 1", failed)
	}
}

// A hash whose only peers are unreachable dies in the prescreen
// window, not at the full timeout — the incumbent's core early-exit
// behaviour, preserved.
func TestFetchPrescreenDropDeadPeer(t *testing.T) {
	// Port 1 on loopback: nothing listens, dials refuse instantly.
	f := newTestFetcher(t, metadata.Config{
		Workers: 2, Timeout: 30 * time.Second, MaxFiles: 50,
		PrescreenEnabled: true, PrescreenTimeout: 300 * time.Millisecond,
	}, staticSource{addrs: []string{"127.0.0.1:1"}})
	_, _, hash := makeInfo(t, "dead", 1)

	start := time.Now()
	_, ok := f.fetch(context.Background(), hash.HexString())
	elapsed := time.Since(start)
	if ok {
		t.Fatal("fetch unexpectedly succeeded")
	}
	if elapsed < 300*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("prescreen drop after %v, want ~300ms", elapsed)
	}
	if got := f.PrescreenDropped(); got != 1 {
		t.Fatalf("prescreenDropped = %d, want 1", got)
	}
}

// With prescreen off and no peers at all, the fetch rides out the
// budget and counts a timeout — again the incumbent's semantics.
func TestFetchTimeoutNoPeers(t *testing.T) {
	f := newTestFetcher(t, metadata.Config{
		Workers: 2, Timeout: 800 * time.Millisecond, MaxFiles: 50,
	}, staticSource{addrs: nil})
	_, _, hash := makeInfo(t, "nobody", 1)

	start := time.Now()
	_, ok := f.fetch(context.Background(), hash.HexString())
	elapsed := time.Since(start)
	if ok {
		t.Fatal("fetch unexpectedly succeeded")
	}
	if elapsed < 800*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("timeout after %v, want ~800ms", elapsed)
	}
	if _, timedOut, _ := f.Stats(); timedOut != 1 {
		t.Fatalf("timedOut = %d, want 1", timedOut)
	}
}

// The pool contract end to end: hashes pushed into a Queue come out
// as Records through RunQueue.
func TestRunQueueDelivers(t *testing.T) {
	_, raw, hash := makeInfo(t, "queued", 3)
	peer := startTestPeer(t, hash, raw)
	f := newTestFetcher(t, metadata.Config{
		Workers: 2, Timeout: 10 * time.Second, MaxFiles: 50,
	}, staticSource{addrs: []string{peer.addr()}})

	q := metadata.NewQueue(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan metadata.Record, 1)
	f.RunQueue(ctx, q, func(rec metadata.Record) { got <- rec })
	q.Push(metadata.Item{Hash: hash.HexString(), Seeders: -1, Source: metadata.SourceDHT})
	select {
	case rec := <-got:
		if rec.InfoHash != hash.HexString() || rec.FileCount != 3 {
			t.Fatalf("record = %+v", rec)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no record delivered")
	}
}
