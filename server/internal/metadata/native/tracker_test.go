package native

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anacrolix/torrent/bencode"
)

func buildAnnounceResponse(txn uint32, peers ...[6]byte) []byte {
	out := make([]byte, 20)
	binary.BigEndian.PutUint32(out[0:4], actionAnnounce)
	binary.BigEndian.PutUint32(out[4:8], txn)
	binary.BigEndian.PutUint32(out[8:12], 60) // interval
	binary.BigEndian.PutUint32(out[12:16], 3)
	binary.BigEndian.PutUint32(out[16:20], 7)
	for _, p := range peers {
		out = append(out, p[:]...)
	}
	return out
}

func TestParseUDPAnnounceResponse(t *testing.T) {
	resp := buildAnnounceResponse(42,
		[6]byte{1, 2, 3, 4, 0x1A, 0xE1}, // 1.2.3.4:6881
		[6]byte{5, 6, 7, 8, 0, 80},      // 5.6.7.8:80
		[6]byte{9, 9, 9, 9, 0, 0},       // port 0: skipped
	)
	peers, err := parseUDPAnnounceResponse(resp, 42)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1.2.3.4:6881", "5.6.7.8:80"}
	if len(peers) != len(want) {
		t.Fatalf("peers = %v, want %v", peers, want)
	}
	for i := range want {
		if peers[i] != want[i] {
			t.Fatalf("peers = %v, want %v", peers, want)
		}
	}
}

func TestParseUDPAnnounceResponseErrors(t *testing.T) {
	if _, err := parseUDPAnnounceResponse(buildAnnounceResponse(42), 43); err == nil {
		t.Fatal("txn mismatch accepted")
	}
	errResp := make([]byte, 16)
	binary.BigEndian.PutUint32(errResp[0:4], actionError)
	copy(errResp[8:], "no such torrent")
	if _, err := parseUDPAnnounceResponse(errResp, 42); err == nil {
		t.Fatal("error action accepted")
	}
	if _, err := parseUDPAnnounceResponse([]byte{1, 2}, 42); err == nil {
		t.Fatal("short response accepted")
	}
}

func TestParseHTTPAnnounceResponseCompact(t *testing.T) {
	body, err := bencode.Marshal(map[string]interface{}{
		"interval": 60,
		"peers":    string([]byte{1, 2, 3, 4, 0x1A, 0xE1, 10, 0, 0, 9, 0, 80}),
	})
	if err != nil {
		t.Fatal(err)
	}
	peers, err := parseHTTPAnnounceResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 || peers[0] != "1.2.3.4:6881" || peers[1] != "10.0.0.9:80" {
		t.Fatalf("peers = %v", peers)
	}
}

func TestParseHTTPAnnounceResponseDictModel(t *testing.T) {
	body, err := bencode.Marshal(map[string]interface{}{
		"peers": []interface{}{
			map[string]interface{}{"ip": "203.0.113.7", "port": 51413},
			map[string]interface{}{"ip": "198.51.100.2", "port": 0}, // invalid port: skipped
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	peers, err := parseHTTPAnnounceResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0] != "203.0.113.7:51413" {
		t.Fatalf("peers = %v", peers)
	}
}

func TestParseHTTPAnnounceResponseFailure(t *testing.T) {
	body, err := bencode.Marshal(map[string]interface{}{"failure reason": "tracker gone"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseHTTPAnnounceResponse(body); err == nil {
		t.Fatal("failure response accepted")
	}
}

// The full HTTP announce against a local server: the request must
// carry the raw infohash and compact=1, and the compact reply must
// come back as peers.
func TestAnnounceHTTP(t *testing.T) {
	var ih [20]byte
	for i := range ih {
		ih[i] = byte(i + 1)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("info_hash"); got != string(ih[:]) {
			t.Errorf("info_hash param = %q, want raw hash bytes", got)
		}
		if q.Get("compact") != "1" {
			t.Errorf("compact = %q, want 1", q.Get("compact"))
		}
		body, err := bencode.Marshal(map[string]interface{}{
			"peers": string([]byte{127, 0, 0, 1, 0x1A, 0xE1}),
		})
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	var pid [20]byte
	peers, err := announceHTTP(context.Background(), srv.Client(), srv.URL+"/announce", ih, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0] != "127.0.0.1:6881" {
		t.Fatalf("peers = %v", peers)
	}
}
