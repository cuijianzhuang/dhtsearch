package libtorrent

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dhtsearch/server/internal/metadata"
)

// fakeBin is the compiled fake sidecar (testdata/fakesidecar), built
// once by TestMain. Empty when the go tool is unavailable, in which
// case the process-level tests skip.
var fakeBin string

func TestMain(m *testing.M) {
	if goTool, err := exec.LookPath("go"); err == nil {
		dir, err := os.MkdirTemp("", "fakesidecar")
		if err == nil {
			bin := filepath.Join(dir, "fakesidecar")
			cmd := exec.Command(goTool, "build", "-o", bin, "./testdata/fakesidecar")
			if out, err := cmd.CombinedOutput(); err == nil {
				fakeBin = bin
			} else {
				println("fakesidecar build failed:", string(out))
			}
		}
	}
	os.Exit(m.Run())
}

const testHash = "0123456789abcdef0123456789abcdef01234567"

func testConfig() Config {
	return Config{
		Config: metadata.Config{
			Workers:          2,
			Timeout:          5 * time.Second,
			PrescreenEnabled: true,
			PrescreenTimeout: 10 * time.Second,
			Logger:           log.New(os.Stderr, "test: ", 0),
		},
		SidecarPath: fakeBin,
	}
}

func requireFake(t *testing.T) {
	t.Helper()
	if fakeBin == "" {
		t.Skip("fake sidecar not built (go tool unavailable)")
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestFetchOK(t *testing.T) {
	requireFake(t)
	t.Setenv("FAKE_SIDECAR_MODE", "ok")
	f, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan string, 1)
	in <- testHash
	records := make(chan metadata.Record, 1)
	f.Run(ctx, in, func(r metadata.Record) { records <- r })

	select {
	case r := <-records:
		if r.InfoHash != testHash || r.Name != "Fake Torrent" || r.TotalSize != 12345 ||
			r.FileCount != 2 || len(r.Files) != 2 || r.Files[0].Path != "dir/a.bin" || r.Files[1].Size != 2345 {
			t.Fatalf("record = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no record")
	}
	if got, _, _ := f.Stats(); got != 1 {
		t.Fatalf("fetched = %d, want 1", got)
	}
}

func TestFetchPrescreenDrop(t *testing.T) {
	requireFake(t)
	t.Setenv("FAKE_SIDECAR_MODE", "nopeer")
	cfg := testConfig()
	cfg.Timeout = 30 * time.Second // the prescreen must fire long before this
	cfg.PrescreenTimeout = 150 * time.Millisecond
	f, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan string, 1)
	in <- testHash
	start := time.Now()
	f.Run(ctx, in, func(r metadata.Record) { t.Errorf("unexpected record %+v", r) })
	waitFor(t, "prescreen drop", func() bool { return f.PrescreenDropped() == 1 })
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("prescreen drop took %s, want well under the 30s timeout", el)
	}
	if got, _, _ := f.Stats(); got != 0 {
		t.Fatalf("fetched = %d, want 0", got)
	}
}

func TestFetchTimeout(t *testing.T) {
	requireFake(t)
	t.Setenv("FAKE_SIDECAR_MODE", "noreply")
	cfg := testConfig()
	cfg.Timeout = 300 * time.Millisecond
	cfg.PrescreenEnabled = false
	f, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan string, 1)
	in <- testHash
	f.Run(ctx, in, func(r metadata.Record) { t.Errorf("unexpected record %+v", r) })
	waitFor(t, "timeout", func() bool {
		_, timedOut, _ := f.Stats()
		return timedOut == 1
	})
}

func TestSidecarDiesMidJobAndRestarts(t *testing.T) {
	requireFake(t)
	t.Setenv("FAKE_SIDECAR_MODE", "die")
	t.Setenv("FAKE_SIDECAR_STATE", filepath.Join(t.TempDir(), "incarnated"))
	f, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan string, 2)
	records := make(chan metadata.Record, 1)
	f.Run(ctx, in, func(r metadata.Record) { records <- r })

	// First job: the sidecar exits mid-flight; the job must be
	// classified as failed (not hang, not time out).
	in <- testHash
	waitFor(t, "failed classification", func() bool {
		_, _, failed := f.Stats()
		return failed == 1
	})

	// The supervisor restarts the sidecar; the second incarnation
	// behaves, so the next job succeeds.
	in <- testHash
	select {
	case r := <-records:
		if r.Name != "Fake Torrent" {
			t.Fatalf("record = %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no record after sidecar restart")
	}
}

func TestNewMissingBinary(t *testing.T) {
	cfg := testConfig()
	cfg.SidecarPath = filepath.Join(t.TempDir(), "no-such-binary")
	_, err := New(cfg)
	if err == nil {
		t.Fatal("New succeeded with a missing sidecar binary")
	}
	if !strings.Contains(err.Error(), BinaryName) || !strings.Contains(err.Error(), "META_LT_SIDECAR") {
		t.Fatalf("error does not name the missing piece: %v", err)
	}
}

func TestNewSidecarNeverReady(t *testing.T) {
	requireFake(t)
	t.Setenv("FAKE_SIDECAR_MODE", "noready")
	old := startupWait
	startupWait = 3 * time.Second
	defer func() { startupWait = old }()

	start := time.Now()
	_, err := New(testConfig())
	if err == nil {
		t.Fatal("New succeeded with a sidecar that never becomes ready")
	}
	if el := time.Since(start); el > 15*time.Second {
		t.Fatalf("New took %s to fail", el)
	}
	if !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("error = %v", err)
	}
}

// TestUnavailableReasonGraceAndClose pins the supervisor's
// reporting rules: no episode = no reason; an episode younger than
// downGrace (a normal restart) = no reason; a sustained episode =
// a reason naming the failure; after Close = no reason (shutdown
// is not an outage).
func TestUnavailableReasonGraceAndClose(t *testing.T) {
	sup := newSupervisor("/nonexistent", log.New(io.Discard, "", 0))
	if r := sup.unavailableReason(); r != "" {
		t.Fatalf("reason before any episode = %q", r)
	}
	sup.downSince = time.Now().Add(-time.Minute)
	sup.lastDownErr = "sidecar process exited"
	r := sup.unavailableReason()
	if !strings.Contains(r, "unavailable") || !strings.Contains(r, "sidecar process exited") {
		t.Fatalf("sustained episode reason = %q", r)
	}
	sup.downSince = time.Now()
	if r := sup.unavailableReason(); r != "" {
		t.Fatalf("reason inside the grace window = %q", r)
	}
	sup.downSince = time.Now().Add(-time.Minute)
	sup.closed = true
	if r := sup.unavailableReason(); r != "" {
		t.Fatalf("reason after close = %q", r)
	}
}

// TestSidecarPermanentlyDeadSurfacesUnavailable is the mid-run
// case: the sidecar starts fine, then dies and every respawn fails
// (here: the binary is swapped for one that exits immediately, the
// same shape as a deleted/corrupted binary). The supervisor keeps
// restarting it forever — it never gives up — so the fetcher must
// surface the outage via UnavailableReason instead of just failing
// every fetch in silence.
func TestSidecarPermanentlyDeadSurfacesUnavailable(t *testing.T) {
	requireFake(t)
	old := downGrace
	downGrace = 100 * time.Millisecond
	defer func() { downGrace = old }()

	// A private copy of the fake sidecar, so the test can replace
	// the binary under the running supervisor.
	dir := t.TempDir()
	sidecar := filepath.Join(dir, "sidecar")
	raw, err := os.ReadFile(fakeBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SIDECAR_MODE", "die")
	t.Setenv("FAKE_SIDECAR_STATE", filepath.Join(dir, "incarnated"))

	cfg := testConfig()
	cfg.SidecarPath = sidecar
	f, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()
	if r := f.UnavailableReason(); r != "" {
		t.Fatalf("reason while sidecar is up = %q", r)
	}

	// Replace the binary (rename over: the running process keeps
	// its inode) with one that exits before the ready frame.
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bad, sidecar); err != nil {
		t.Fatal(err)
	}

	// The first fetch kills incarnation 0 (fake "die" mode); every
	// respawn now fails instantly, so the outage must surface and
	// the fetch must classify as failed, not hang.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan string, 1)
	f.Run(ctx, in, func(r metadata.Record) { t.Errorf("unexpected record %+v", r) })
	in <- testHash
	waitFor(t, "failed classification", func() bool {
		_, _, failed := f.Stats()
		return failed == 1
	})
	waitFor(t, "unavailable reason", func() bool { return f.UnavailableReason() != "" })
	if r := f.UnavailableReason(); !strings.Contains(r, "unavailable") {
		t.Fatalf("reason = %q", r)
	}
}

func TestInvalidHashCountsFailed(t *testing.T) {
	requireFake(t)
	t.Setenv("FAKE_SIDECAR_MODE", "ok")
	f, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan string, 1)
	in <- "not-a-hash"
	f.Run(ctx, in, func(r metadata.Record) { t.Errorf("unexpected record %+v", r) })
	waitFor(t, "failed count", func() bool {
		_, _, failed := f.Stats()
		return failed == 1
	})
}

func TestRunQueue(t *testing.T) {
	requireFake(t)
	t.Setenv("FAKE_SIDECAR_MODE", "ok")
	f, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := metadata.NewQueue(0)
	q.Push(metadata.Item{Hash: testHash, Seeders: -1, Source: metadata.SourceDHT, At: time.Now()})
	records := make(chan metadata.Record, 1)
	f.RunQueue(ctx, q, func(r metadata.Record) { records <- r })
	select {
	case r := <-records:
		if r.Name != "Fake Torrent" {
			t.Fatalf("record = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no record via queue")
	}
}

func TestMagnetSuffixParity(t *testing.T) {
	got := magnetTrackerParams([]string{"udp://a.example:1337/announce", " https://b.example/y ", "", "udp://c.example:6969"})
	want := "&tr=udp%3A%2F%2Fa.example%3A1337%2Fannounce&tr=https%3A%2F%2Fb.example%2Fy&tr=udp%3A%2F%2Fc.example%3A6969"
	if got != want {
		t.Fatalf("magnetTrackerParams = %q, want %q", got, want)
	}
}

// TestDHTStatusReported: the sidecar's status frames land in
// Fetcher.DHTStatus — the admin surface's proof that the sidecar's
// own DHT is running and its routing table populated.
func TestDHTStatusReported(t *testing.T) {
	requireFake(t)
	t.Setenv("FAKE_SIDECAR_MODE", "ok")
	f, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()
	waitFor(t, "DHT status", func() bool {
		running, nodes := f.DHTStatus()
		return running && nodes == 42
	})
}

// TestRunQueuePeerHintsForwarded: a queue item's Peers (the #46
// peer-hint cache contents) must cross the wire as the fetch frame's
// peer_hints — the libtorrent half of the hint supply the incumbent
// already consumes.
func TestRunQueuePeerHintsForwarded(t *testing.T) {
	requireFake(t)
	t.Setenv("FAKE_SIDECAR_MODE", "ok")
	hintsLog := filepath.Join(t.TempDir(), "hints.log")
	t.Setenv("FAKE_SIDECAR_HINTS_LOG", hintsLog)
	f, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := metadata.NewQueue(0)
	hints := []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.7:6881"),
		netip.MustParseAddrPort("[2001:db8::9]:51413"),
	}
	q.Push(metadata.Item{Hash: testHash, Seeders: -1, Source: metadata.SourceDHT, At: time.Now(), Peers: hints})
	records := make(chan metadata.Record, 1)
	f.RunQueue(ctx, q, func(r metadata.Record) { records <- r })
	select {
	case <-records:
	case <-time.After(5 * time.Second):
		t.Fatal("no record via queue")
	}
	raw, err := os.ReadFile(hintsLog)
	if err != nil {
		t.Fatalf("read hints log: %v", err)
	}
	for _, h := range hints {
		if !strings.Contains(string(raw), h.String()) {
			t.Fatalf("hints log %q does not contain %q", raw, h.String())
		}
	}
}

func TestFrameCodec(t *testing.T) {
	var buf bytes.Buffer
	in := &frame{Type: "fetch", ID: 42, Magnet: "magnet:?xt=urn:btih:abc", Deadline: 45000, MaxFiles: 50,
		PeerHints: []string{"203.0.113.7:6881", "[2001:db8::9]:51413"}}
	if err := writeFrame(&buf, in); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	out, err := readFrame(&buf)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if out.Type != in.Type || out.ID != in.ID || out.Magnet != in.Magnet || out.Deadline != in.Deadline || out.MaxFiles != in.MaxFiles {
		t.Fatalf("roundtrip = %+v, want %+v", out, in)
	}
	if len(out.PeerHints) != 2 || out.PeerHints[0] != in.PeerHints[0] || out.PeerHints[1] != in.PeerHints[1] {
		t.Fatalf("peer hints roundtrip = %v, want %v", out.PeerHints, in.PeerHints)
	}

	// A fetch frame from an older server (no peer_hints field at
	// all) must still decode, with no hints.
	var legacy bytes.Buffer
	legacy.WriteString(`{"type":"fetch","id":7,"magnet":"magnet:?xt=urn:btih:abc","deadline_ms":45000,"max_files":50}`)
	var lb bytes.Buffer
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(legacy.Len()))
	lb.Write(hdr[:])
	lb.Write(legacy.Bytes())
	out, err = readFrame(&lb)
	if err != nil {
		t.Fatalf("readFrame legacy: %v", err)
	}
	if out.ID != 7 || len(out.PeerHints) != 0 {
		t.Fatalf("legacy frame = %+v", out)
	}

	// A status frame roundtrips its DHT fields.
	var sb bytes.Buffer
	if err := writeFrame(&sb, &frame{Type: "status", DHTRunning: true, DHTNodes: 321}); err != nil {
		t.Fatalf("writeFrame status: %v", err)
	}
	out, err = readFrame(&sb)
	if err != nil {
		t.Fatalf("readFrame status: %v", err)
	}
	if out.Type != "status" || !out.DHTRunning || out.DHTNodes != 321 {
		t.Fatalf("status roundtrip = %+v", out)
	}

	// A corrupt length prefix must be rejected, not allocated.
	var bad bytes.Buffer
	bad.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	if _, err := readFrame(&bad); err == nil {
		t.Fatal("readFrame accepted an out-of-bounds length")
	}
}
