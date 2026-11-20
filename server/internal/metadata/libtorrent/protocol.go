package libtorrent

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Wire protocol between the Go server and the libtorrent sidecar
// process (sidecar/PROTOCOL.md is the normative document; this file is
// its Go half).
//
// Transport: the sidecar's stdin/stdout. Every message is one frame —
// a 4-byte big-endian length prefix followed by that many bytes of
// UTF-8 JSON. stderr is reserved for the sidecar's own logging and is
// forwarded to the server log by the supervisor.
//
// Message flow:
//
//	server -> sidecar  {"type":"fetch","id":N,"magnet":"...","deadline_ms":45000,"max_files":50,
//	                     "peer_hints":["1.2.3.4:6881","[2001:db8::1]:6881"]}
//	server -> sidecar  {"type":"cancel","id":N}
//	server -> sidecar  {"type":"shutdown"}
//	sidecar -> server  {"type":"ready","sidecar":"x.y.z","libtorrent":"2.0.x"}
//	sidecar -> server  {"type":"peers","id":N,"peers":K}     (first peer, then on change)
//	sidecar -> server  {"type":"status","dht_running":true,"dht_nodes":N}
//	                     (the sidecar session's own DHT state, on change)
//	sidecar -> server  {"type":"result","id":N,"outcome":"ok","name":"...",
//	                     "total_size":N,"file_count":N,
//	                     "files":[{"path":"...","size":N}]}
//	sidecar -> server  {"type":"result","id":N,"outcome":"timeout","peers":K}
//	sidecar -> server  {"type":"result","id":N,"outcome":"error","error":"..."}
//
// The sidecar enforces deadline_ms itself (it removes the torrent and
// answers outcome=timeout), but the Go side keeps its own timers for
// the prescreen phase and as a backstop: whichever side concludes a
// job first decides its classification, and late frames for a finished
// job are dropped.

// maxFrameBytes bounds one frame. Results carry at most MaxFiles file
// entries (the sidecar truncates, reporting the true count in
// file_count), so a frame stays small; the cap is a sanity rail
// against a corrupt or hostile stream desynchronising the reader.
const maxFrameBytes = 4 << 20

// frame is the decoded envelope of one wire message. Only the fields
// relevant to the message type are populated.
type frame struct {
	Type       string     `json:"type"`
	ID         uint64     `json:"id,omitempty"`
	Magnet     string     `json:"magnet,omitempty"`
	Deadline   int64      `json:"deadline_ms,omitempty"`
	MaxFiles   int        `json:"max_files,omitempty"`
	Peers      int        `json:"peers,omitempty"`
	Outcome    string     `json:"outcome,omitempty"`
	Name       string     `json:"name,omitempty"`
	TotalSize  int64      `json:"total_size,omitempty"`
	FileCount  int        `json:"file_count,omitempty"`
	Files      []wireFile `json:"files,omitempty"`
	Error      string     `json:"error,omitempty"`
	Sidecar    string     `json:"sidecar,omitempty"`
	Libtorrent string     `json:"libtorrent,omitempty"`
	// PeerHints rides a fetch frame server -> sidecar: initial peer
	// addresses ("host:port") the sidecar connects alongside its
	// tracker/PEX/DHT supply — the #46 peer-hint cache contents for
	// this hash (metadata.Item.Peers). Absent means no hints; the
	// field postdates the original protocol, so an older sidecar
	// simply ignores it and an older server never sends it.
	PeerHints []string `json:"peer_hints,omitempty"`
	// DHTRunning / DHTNodes ride a status frame sidecar -> server:
	// the sidecar session's own DHT state (running, routing-table
	// node count), surfaced to the admin via Fetcher.DHTStatus.
	DHTRunning bool `json:"dht_running,omitempty"`
	DHTNodes   int  `json:"dht_nodes,omitempty"`
}

type wireFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Result outcomes reported by the sidecar.
const (
	outcomeOK      = "ok"
	outcomeTimeout = "timeout"
	outcomeError   = "error"
)

// writeFrame encodes one frame to w. Callers serialise writes.
func writeFrame(w io.Writer, f *frame) error {
	body, err := json.Marshal(f)
	if err != nil {
		return err
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// readFrame decodes one frame from r.
func readFrame(r io.Reader) (*frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > maxFrameBytes {
		return nil, fmt.Errorf("frame length %d out of bounds", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	var f frame
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("frame body: %w", err)
	}
	return &f, nil
}
