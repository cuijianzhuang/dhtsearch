package metadata

import (
	"bytes"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// The zero infohash reaches AddMagnet as a panic, not an error, so rejecting
// it before the library sees it is what keeps a worker from taking the
// process down.
func TestValidInfohash(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"typical", "97a29535b9e2f0e46a1b09a4b0c0e4f0a1b2c3d4", true},
		{"uppercase", "97A29535B9E2F0E46A1B09A4B0C0E4F0A1B2C3D4", true},
		{"all zeros", "0000000000000000000000000000000000000000", false},
		{"empty", "", false},
		{"too short", "97a29535", false},
		{"too long", "97a29535b9e2f0e46a1b09a4b0c0e4f0a1b2c3d4f", false},
		{"non-hex", "97a29535b9e2f0e46a1b09a4b0c0e4f0a1b2c3zz", false},
		{"leading zeros are fine", "0000000000000000000000000000000000000001", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validInfohash(tc.in); got != tc.want {
				t.Fatalf("validInfohash(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// The bitfield claims piece 1 before a one-piece torrent's info arrives.
// v1.61.0 mistakes its cardinality for a complete peer, increments piece 0,
// then truncates piece 1. Dropping used to panic with relative availability 1.
func TestDropAfterInvalidPremetadataBitfield(t *testing.T) {
	cfg := metadataClientConfig()
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.DisableUTP = true
	cfg.DisableIPv6 = true
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	received := make(chan struct{}, 1)
	cfg.Callbacks.ReadMessage = func(pc *torrent.PeerConn, msg *pp.Message) {
		ignorePayloadAvailability(pc, msg)
		if msg.Type == pp.Bitfield {
			received <- struct{}{}
		}
	}
	client, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	info := metainfo.Info{Name: strings.Repeat("x", 300), PieceLength: 16384, Length: 1, Pieces: make([]byte, 20)}
	raw, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	hash := metainfo.HashBytes(raw)
	tor, _, err := client.AddTorrentSpec(&torrent.TorrentSpec{AddTorrentOpts: torrent.AddTorrentOpts{
		InfoHash: hash, DisallowDataDownload: true, DisallowDataUpload: true, DisableInitialPieceCheck: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", client.ListenAddrs()[0].String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	handshake := append([]byte("\x13BitTorrent protocol"), make([]byte, 8)...)
	handshake = append(handshake, hash[:]...)
	handshake = append(handshake, []byte("-TEST00-123456789012")...)
	if _, err := conn.Write(handshake); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 68)); err != nil {
		t.Fatal(err)
	}
	// Length=2, type=bitfield, only the second piece is advertised.
	if _, err := conn.Write([]byte{0, 0, 0, 2, 5, 0x40}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("bitfield not received")
	}
	if err := tor.SetInfoBytes(raw); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tor.GotInfo():
	case <-time.After(5 * time.Second):
		t.Fatal("metadata not ready")
	}
	if tor.Info().BestName() != info.Name {
		t.Fatal("metadata title changed")
	}
	tor.Drop()
	if len(client.Torrents()) != 0 {
		t.Fatal("dropped torrent retained")
	}
}

func TestMetadataStorageRejectsPayload(t *testing.T) {
	st, err := (metadataStorage{}).OpenTorrent(t.Context(), &metainfo.Info{}, metainfo.Hash{})
	if err != nil {
		t.Fatal(err)
	}
	piece := st.Piece(metainfo.Piece{})
	if n, err := piece.WriteAt([]byte("payload"), 0); n != 0 || err == nil {
		t.Fatal("payload write accepted")
	}
	if n, err := piece.ReadAt(make([]byte, 1), 0); n != 0 || err == nil {
		t.Fatal("payload read accepted")
	}
	if c := piece.Completion(); !c.Ok || c.Complete {
		t.Fatalf("completion = %+v", c)
	}
}

func TestMetadataProtocolMessagesRemainEnabled(t *testing.T) {
	for _, typ := range []pp.MessageType{pp.Extended, pp.Choke, pp.Unchoke, pp.Port} {
		msg := pp.Message{Type: typ}
		ignorePayloadAvailability(nil, &msg)
		if msg.Keepalive {
			t.Fatalf("message %v suppressed", typ)
		}
	}
}

func TestFetchMetadataFromLocalPeer(t *testing.T) {
	newClient := func() *torrent.Client {
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
	source, destination := newClient(), newClient()
	info := metainfo.Info{Name: strings.Repeat("x", 300), PieceLength: 16384, Length: 1, Pieces: make([]byte, 20)}
	raw, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	hash := metainfo.HashBytes(raw)
	sourceTorrent, _, err := source.AddTorrentSpec(&torrent.TorrentSpec{AddTorrentOpts: torrent.AddTorrentOpts{
		InfoHash: hash, InfoBytes: raw, DisallowDataUpload: true, DisableInitialPieceCheck: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Keep the source willing to accept connections despite its empty storage.
	sourceTorrent.DownloadAll()
	tor, _, err := destination.AddTorrentSpec(&torrent.TorrentSpec{AddTorrentOpts: torrent.AddTorrentOpts{
		InfoHash: hash, DisallowDataDownload: true, DisallowDataUpload: true, DisableInitialPieceCheck: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	tor.AddPeers([]torrent.PeerInfo{{Addr: source.ListenAddrs()[0], Trusted: true}})
	f := &Fetcher{client: destination, cfg: Config{Timeout: 5 * time.Second, MaxFiles: 50}}
	record, ok := f.fetch(t.Context(), hash.HexString())
	if !ok {
		var status bytes.Buffer
		source.WriteStatus(&status)
		destination.WriteStatus(&status)
		t.Fatalf("metadata fetch failed: %s", status.String())
	}
	if record.Name != info.Name || record.TotalSize != 1 || record.FileCount != 1 {
		t.Fatalf("record=%+v", record)
	}
	if len(destination.Torrents()) != 0 {
		t.Fatal("fetch did not drop torrent")
	}
	stats := destination.Stats()
	if n := stats.BytesReadData.Int64(); n != 0 {
		t.Fatalf("downloaded %d payload bytes", n)
	}
}
