package native

import (
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"dhtsearch/server/internal/metadata"
)

// recordFromRaw must produce exactly the Record the incumbent builds
// from the same info dict (both go through metadata.RecordFromInfo);
// these fixtures pin the native side's hash-check + parse path for
// the single-file and multi-file shapes.
func TestRecordFromRawFixtures(t *testing.T) {
	f, err := New(metadata.Config{Workers: 1, MaxFiles: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	single := metainfo.Info{Name: "single-file.iso", PieceLength: 16384, Length: 987654, Pieces: make([]byte, 20)}
	multi := metainfo.Info{Name: "multi-pack", PieceLength: 16384, Pieces: make([]byte, 20)}
	for i := 0; i < 5; i++ {
		multi.Files = append(multi.Files, metainfo.FileInfo{
			Length: int64(100 * (i + 1)),
			Path:   []string{"sub", "part" + string(rune('a'+i)) + ".mkv"},
		})
	}
	for _, info := range []metainfo.Info{single, multi} {
		raw, err := bencode.Marshal(info)
		if err != nil {
			t.Fatal(err)
		}
		hash := metainfo.HashBytes(raw)
		rec, ok := f.recordFromRaw(hash, hash.HexString(), raw)
		if !ok {
			t.Fatalf("recordFromRaw rejected %q", info.Name)
		}
		want := metadata.RecordFromInfo(&info, hash.HexString(), 3)
		if rec.Name != want.Name || rec.TotalSize != want.TotalSize || rec.FileCount != want.FileCount {
			t.Fatalf("record = %+v, want %+v", rec, want)
		}
		if len(rec.Files) != len(want.Files) {
			t.Fatalf("files = %d, want %d (MaxFiles cap)", len(rec.Files), len(want.Files))
		}
		for i := range want.Files {
			if rec.Files[i] != want.Files[i] {
				t.Fatalf("file %d = %+v, want %+v", i, rec.Files[i], want.Files[i])
			}
		}
		// The same bytes under a different hash must be refused.
		var other [20]byte
		copy(other[:], hash[:])
		other[0] ^= 0xFF
		if _, ok := f.recordFromRaw(other, hash.HexString(), raw); ok {
			t.Fatalf("recordFromRaw accepted %q under a foreign hash", info.Name)
		}
	}
}

// splitDict must hand back exactly the bytes after the dictionary —
// the piece payload — even when the data itself starts with bytes
// that look like bencode.
func TestSplitDict(t *testing.T) {
	head, err := bencode.Marshal(metaHeader{MsgType: metaData, Piece: 3, TotalSize: 40000})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("d9:looks-like-a-dict-but-is-piece-data")
	payload := append(append([]byte{}, head...), data...)
	hdr, rest, err := splitDict(payload)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.MsgType != metaData || hdr.Piece != 3 || hdr.TotalSize != 40000 {
		t.Fatalf("hdr = %+v", hdr)
	}
	if string(rest) != string(data) {
		t.Fatalf("rest = %q, want %q", rest, data)
	}
}

func TestParseCompactPeersEdges(t *testing.T) {
	// Trailing partial entry ignored; zero port skipped.
	got := parseCompactPeers([]byte{1, 1, 1, 1, 0, 80, 2, 2, 2, 2, 0, 0, 3, 3}, 4)
	if len(got) != 1 || got[0] != "1.1.1.1:80" {
		t.Fatalf("peers = %v", got)
	}
	if got := parseCompactPeers(nil, 4); got != nil {
		t.Fatalf("peers = %v, want nil", got)
	}
	// IPv6 form.
	v6 := make([]byte, 0, 18)
	v6 = append(v6, []byte{0x20, 0x01, 0xd, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}...)
	v6 = append(v6, 0x1A, 0xE1)
	got = parseCompactPeers(v6, 16)
	if len(got) != 1 || got[0] != "[2001:db8::1]:6881" {
		t.Fatalf("peers6 = %v", got)
	}
}
