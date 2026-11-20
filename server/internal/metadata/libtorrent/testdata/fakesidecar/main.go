// Fake libtorrent sidecar for the parent package's tests. It speaks
// the real wire protocol (sidecar/PROTOCOL.md) on stdin/stdout; its
// per-job behaviour is selected with FAKE_SIDECAR_MODE:
//
//	ok       peers event, then an ok result with a fixed record
//	nopeer   never reports peers; answers timeout at the job deadline
//	noreply  never answers jobs at all (the Go-side timers must fire)
//	die      exits(2) on the first fetch of the first incarnation;
//	         later incarnations (tracked via FAKE_SIDECAR_STATE)
//	         behave like ok — exercises supervisor restart
//	noready  exits(1) immediately, before the ready frame
//
// This file lives in testdata so it is never part of the server
// build; the tests compile it on demand.
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

type frame struct {
	Type       string     `json:"type"`
	ID         uint64     `json:"id,omitempty"`
	Deadline   int64      `json:"deadline_ms,omitempty"`
	Sidecar    string     `json:"sidecar,omitempty"`
	Libtorrent string     `json:"libtorrent,omitempty"`
	Peers      int        `json:"peers,omitempty"`
	Outcome    string     `json:"outcome,omitempty"`
	Name       string     `json:"name,omitempty"`
	TotalSize  int64      `json:"total_size,omitempty"`
	FileCount  int        `json:"file_count,omitempty"`
	Files      []wireFile `json:"files,omitempty"`
}

type wireFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

var (
	out   = bufio.NewWriter(os.Stdout)
	outMu sync.Mutex
)

func send(f *frame) {
	outMu.Lock()
	defer outMu.Unlock()
	body, _ := json.Marshal(f)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	out.Write(hdr[:])
	out.Write(body)
	out.Flush()
}

func recv(r io.Reader) (*frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	body := make([]byte, binary.BigEndian.Uint32(hdr[:]))
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	var f frame
	return &f, json.Unmarshal(body, &f)
}

func main() {
	mode := os.Getenv("FAKE_SIDECAR_MODE")
	if mode == "noready" {
		os.Exit(1)
	}
	incarnation := 0
	if mode == "die" {
		state := os.Getenv("FAKE_SIDECAR_STATE")
		if state != "" {
			if _, err := os.Stat(state); err == nil {
				incarnation = 1
			} else {
				_ = os.WriteFile(state, []byte("1"), 0o644)
			}
		}
	}
	send(&frame{Type: "ready", Sidecar: "fake-1.0", Libtorrent: "2.0.0-fake"})
	for {
		f, err := recv(os.Stdin)
		if err != nil {
			return
		}
		switch f.Type {
		case "shutdown":
			return
		case "cancel":
			// forget the job; nothing to do — replies are keyed by id
			// and the parent drops late results anyway.
		case "fetch":
			switch {
			case mode == "die" && incarnation == 0:
				os.Exit(2)
			case mode == "noreply":
				// silence
			case mode == "nopeer":
				id, dl := f.ID, f.Deadline
				go func() {
					time.Sleep(time.Duration(dl) * time.Millisecond)
					send(&frame{Type: "result", ID: id, Outcome: "timeout", Peers: 0})
				}()
			default: // ok
				id := f.ID
				go func() {
					time.Sleep(20 * time.Millisecond)
					send(&frame{Type: "peers", ID: id, Peers: 3})
					time.Sleep(30 * time.Millisecond)
					send(&frame{Type: "result", ID: id, Outcome: "ok",
						Name: "Fake Torrent", TotalSize: 12345, FileCount: 2,
						Files: []wireFile{
							{Path: "dir/a.bin", Size: 10000},
							{Path: "dir/b.bin", Size: 2345},
						}})
				}()
			}
		}
	}
}
