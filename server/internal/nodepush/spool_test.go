package nodepush

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/scraper"
)

func spoolPusher(t *testing.T, client *Client, dir string) *Pusher {
	t.Helper()
	p := NewPusher(client, testLogger())
	if err := p.EnableSpool(dir, 1<<30); err != nil {
		t.Fatalf("EnableSpool: %v", err)
	}
	return p
}

func spoolFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if _, ok := parseSegmentName(e.Name()); ok {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func testRecord(hashChar rune, name string) metadata.Record {
	return metadata.Record{
		InfoHash:  strings.Repeat(string(hashChar), 40),
		Name:      name,
		TotalSize: 1 << 30,
		FileCount: 1,
		Files:     []filter.File{{Path: "f.mkv", Size: 1 << 30}},
		Source:    metadata.SourceDHT,
	}
}

func ingestedHashes(fc *fakeCenter) []string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	var out []string
	for _, ing := range fc.ingests {
		for _, r := range ing.Records {
			out = append(out, r.InfoHash)
		}
	}
	return out
}

func setFail(fc *fakeCenter, fail bool) {
	fc.mu.Lock()
	fc.failMode = fail
	fc.mu.Unlock()
}

// A failed flush must land the batch in the spool (not the drop
// counter), and once the center recovers the spool must drain —
// every batch delivered exactly once, segment files gone.
func TestSpoolSpillsOnFailureAndDrains(t *testing.T) {
	fc, client := newFakeCenter(t)
	dir := t.TempDir()
	p := spoolPusher(t, client, dir)

	setFail(fc, true)
	hash := strings.Repeat("a", 40)
	p.Push(testRecord('a', "one"), "")
	p.Push(testRecord('b', "two"), "")
	p.PushSwarm([]scraper.SwarmStat{{Hash: hash, Seeders: 7, Leechers: 1, Completed: 3}})
	p.flush(context.Background())

	if n, _ := p.Buffered(); n != 0 {
		t.Fatalf("buffered after failed flush = %d, want 0 (batch spooled)", n)
	}
	if p.Dropped() != 0 {
		t.Fatalf("Dropped = %d, want 0 (spooling is not dropping)", p.Dropped())
	}
	if p.Spooled() != 2 {
		t.Fatalf("Spooled = %d, want 2", p.Spooled())
	}
	files := spoolFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("spool files = %d, want 1", len(files))
	}
	batches, err := readSegment(files[0])
	if err != nil || len(batches) != 1 {
		t.Fatalf("spooled batches = %d, err %v; want 1", len(batches), err)
	}
	if len(batches[0].Records) != 2 || len(batches[0].Swarm) != 1 || batches[0].Swarm[0].Seeders != 7 {
		t.Fatalf("spooled batch = %+v", batches[0])
	}

	setFail(fc, false)
	p.flush(context.Background())
	got := ingestedHashes(fc)
	if len(got) != 2 {
		t.Fatalf("center received %d records, want exactly 2 (%v)", len(got), got)
	}
	if p.Replayed() != 2 || p.Pushed() != 2 {
		t.Fatalf("Replayed/Pushed = %d/%d, want 2/2", p.Replayed(), p.Pushed())
	}
	if files := spoolFiles(t, dir); len(files) != 0 {
		t.Fatalf("spool files after drain = %d, want 0", len(files))
	}
}

// A spool left behind by one pusher (a "restart") is picked up by
// the next one on the same directory and drained on flush.
func TestSpoolSurvivesRestart(t *testing.T) {
	fc, client := newFakeCenter(t)
	dir := t.TempDir()

	setFail(fc, true)
	p1 := spoolPusher(t, client, dir)
	p1.Push(testRecord('c', "before-restart"), "")
	p1.flush(context.Background())
	if p1.Spooled() != 1 {
		t.Fatalf("Spooled before restart = %d, want 1", p1.Spooled())
	}

	p2 := spoolPusher(t, client, dir)
	if _, _, recs, ok := p2.SpoolDepth(); !ok || recs != 1 {
		t.Fatalf("SpoolDepth after restart = %d records (ok %v), want 1", recs, ok)
	}
	setFail(fc, false)
	p2.flush(context.Background())
	got := ingestedHashes(fc)
	if len(got) != 1 || got[0] != strings.Repeat("c", 40) {
		t.Fatalf("center received %v, want the one pre-restart record", got)
	}
	if files := spoolFiles(t, dir); len(files) != 0 {
		t.Fatalf("spool files after restart drain = %d, want 0", len(files))
	}
}

// With a spool, a memory-buffer overflow during an outage spills
// the oldest records to disk instead of dropping them; everything
// is delivered once the center returns.
func TestSpoolBufferOverflowSpills(t *testing.T) {
	fc, client := newFakeCenter(t)
	dir := t.TempDir()
	p := spoolPusher(t, client, dir)
	p.bufferRecords = 10

	setFail(fc, true)
	for i := 0; i < 25; i++ {
		p.Push(testRecord(rune('a'+i%26), "x"), "")
	}
	if n, _ := p.Buffered(); n > 10 {
		t.Fatalf("buffered = %d, want <= 10 (cap)", n)
	}
	if p.Dropped() != 0 {
		t.Fatalf("Dropped = %d, want 0 (overflow spilled, not dropped)", p.Dropped())
	}
	_, _, spooledRecs, _ := p.SpoolDepth()
	if n, _ := p.Buffered(); int64(n)+spooledRecs != 25 {
		t.Fatalf("buffered %d + spooled %d = %d, want 25", n, spooledRecs, int64(n)+spooledRecs)
	}

	setFail(fc, false)
	p.flush(context.Background())
	got := ingestedHashes(fc)
	if len(got) != 25 {
		t.Fatalf("center received %d records, want 25", len(got))
	}
	seen := map[string]bool{}
	for _, h := range got {
		if seen[h] {
			t.Fatalf("record %s delivered twice", h)
		}
		seen[h] = true
	}
}

// Past the size cap the oldest segments are dropped (and counted),
// the newest data survives.
func TestSpoolCapDropsOldest(t *testing.T) {
	dir := t.TempDir()
	sp, err := OpenSpool(dir, 1<<30, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	sp.segBytes = 300 // ~one batch per segment
	sp.maxBytes = 620 // ~two segments fit
	bigName := strings.Repeat("n", 120)
	mk := func(c rune) spoolBatch {
		return spoolBatch{Records: []RecordWire{{InfoHash: strings.Repeat(string(c), 40), Name: bigName}}}
	}
	for _, c := range []rune{'a', 'b', 'c'} {
		if err := sp.Append(mk(c)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if sp.DroppedRecords() != 1 {
		t.Fatalf("DroppedRecords = %d, want 1 (oldest segment)", sp.DroppedRecords())
	}
	segs, _, recs := sp.Depth()
	if segs != 2 || recs != 2 {
		t.Fatalf("Depth = %d segs / %d records, want 2/2", segs, recs)
	}
	var delivered []string
	err = sp.Replay(context.Background(), func(_ context.Context, b spoolBatch) (*IngestResponse, error) {
		for _, r := range b.Records {
			delivered = append(delivered, r.InfoHash)
		}
		return &IngestResponse{Ingested: len(b.Records)}, nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(delivered) != 2 || delivered[0] != strings.Repeat("b", 40) || delivered[1] != strings.Repeat("c", 40) {
		t.Fatalf("delivered = %v, want [b… c…] (a dropped by the cap)", delivered)
	}
}

// A torn tail (crash mid-append: a length prefix promising more
// than was written, plus garbage) is truncated at open; the valid
// prefix still replays.
func TestSpoolTornTailTruncated(t *testing.T) {
	dir := t.TempDir()
	sp, err := OpenSpool(dir, 1<<30, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	b := spoolBatch{Records: []RecordWire{{InfoHash: strings.Repeat("a", 40), Name: "good"}}}
	if err := sp.Append(b); err != nil {
		t.Fatal(err)
	}
	_ = sp.Close()
	files := spoolFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("spool files = %d, want 1", len(files))
	}
	f, err := os.OpenFile(files[0], os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 100) // promises 100 bytes…
	if _, err := f.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("only-a-few")); err != nil { // …delivers 10, then dies
		t.Fatal(err)
	}
	_ = f.Close()

	sp2, err := OpenSpool(dir, 1<<30, testLogger())
	if err != nil {
		t.Fatalf("OpenSpool with torn tail: %v", err)
	}
	if sp2.Corrupt() != 1 {
		t.Fatalf("Corrupt = %d, want 1", sp2.Corrupt())
	}
	var delivered int
	err = sp2.Replay(context.Background(), func(_ context.Context, b spoolBatch) (*IngestResponse, error) {
		delivered += len(b.Records)
		return &IngestResponse{Ingested: len(b.Records)}, nil
	})
	if err != nil || delivered != 1 {
		t.Fatalf("Replay delivered %d (err %v), want 1", delivered, err)
	}
}

// A corrupt frame mid-segment ends that segment there (later
// frames in it are unreachable); the NEXT segment is unaffected.
func TestSpoolMidSegmentCorruption(t *testing.T) {
	dir := t.TempDir()
	frame := func(b spoolBatch) []byte {
		payload, _ := json.Marshal(b)
		out := make([]byte, 4+len(payload))
		binary.BigEndian.PutUint32(out, uint32(len(payload)))
		copy(out[4:], payload)
		return out
	}
	rec := func(c rune) spoolBatch {
		return spoolBatch{Records: []RecordWire{{InfoHash: strings.Repeat(string(c), 40), Name: "x"}}}
	}
	// The second frame is genuinely undecodable: valid length,
	// invalid JSON.
	bad := make([]byte, 4+3)
	binary.BigEndian.PutUint32(bad, 3)
	copy(bad[4:], "{!!")
	seg1 := append(frame(rec('a')), bad...)
	seg1 = append(seg1, frame(rec('b'))...) // unreachable, behind the bad frame
	if err := os.WriteFile(filepath.Join(dir, segmentName(1)), seg1, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, segmentName(2)), frame(rec('c')), 0o644); err != nil {
		t.Fatal(err)
	}

	sp, err := OpenSpool(dir, 1<<30, testLogger())
	if err != nil {
		t.Fatalf("OpenSpool: %v", err)
	}
	if sp.Corrupt() != 1 {
		t.Fatalf("Corrupt = %d, want 1", sp.Corrupt())
	}
	var delivered []string
	err = sp.Replay(context.Background(), func(_ context.Context, b spoolBatch) (*IngestResponse, error) {
		for _, r := range b.Records {
			delivered = append(delivered, r.InfoHash)
		}
		return &IngestResponse{Ingested: len(b.Records)}, nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	want := []string{strings.Repeat("a", 40), strings.Repeat("c", 40)}
	if len(delivered) != 2 || delivered[0] != want[0] || delivered[1] != want[1] {
		t.Fatalf("delivered = %v, want %v (b lost behind the corrupt frame)", delivered, want)
	}
}

// When pushes succeed, the fast path never touches the disk.
func TestSpoolUntouchedOnSuccess(t *testing.T) {
	_, client := newFakeCenter(t)
	dir := t.TempDir()
	p := spoolPusher(t, client, dir)
	p.Push(testRecord('a', "fine"), "")
	p.flush(context.Background())
	if p.Pushed() != 1 {
		t.Fatalf("Pushed = %d, want 1", p.Pushed())
	}
	if p.Spooled() != 0 {
		t.Fatalf("Spooled = %d, want 0", p.Spooled())
	}
	if files := spoolFiles(t, dir); len(files) != 0 {
		t.Fatalf("spool files on the success path = %d, want 0", len(files))
	}
}

// A batch ACKed before a mid-segment failure is not resent when
// replay resumes within the same session; the segment is deleted
// once its remaining batches land.
func TestSpoolReplayResumesWithinSegment(t *testing.T) {
	dir := t.TempDir()
	sp, err := OpenSpool(dir, 1<<30, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []rune{'a', 'b', 'c'} {
		if err := sp.Append(spoolBatch{Records: []RecordWire{{InfoHash: strings.Repeat(string(c), 40)}}}); err != nil {
			t.Fatal(err)
		}
	}
	var sent []string
	failOnB := true
	send := func(_ context.Context, b spoolBatch) (*IngestResponse, error) {
		h := b.Records[0].InfoHash
		if failOnB && h == strings.Repeat("b", 40) {
			return nil, errors.New("boom")
		}
		sent = append(sent, h)
		return &IngestResponse{Ingested: 1}, nil
	}
	if err := sp.Replay(context.Background(), send); err == nil {
		t.Fatal("first Replay should fail on batch b")
	}
	if len(sent) != 1 || sent[0] != strings.Repeat("a", 40) {
		t.Fatalf("first Replay sent %v, want [a…]", sent)
	}
	failOnB = false
	if err := sp.Replay(context.Background(), send); err != nil {
		t.Fatalf("second Replay: %v", err)
	}
	if len(sent) != 3 || sent[1] != strings.Repeat("b", 40) || sent[2] != strings.Repeat("c", 40) {
		t.Fatalf("total sent = %v, want [a b c] with a sent once", sent)
	}
	if files := spoolFiles(t, dir); len(files) != 0 {
		t.Fatalf("spool files after full replay = %d, want 0", len(files))
	}
}

// TestSpoolOpenEnforcesCap: segments written under a generous cap
// must be trimmed when the spool is reopened under a smaller one —
// previously the cap only applied to new appends, so a lowered
// NODE_SPOOL_MAX_MB left the spool over its bound until the first
// append of the new run.
func TestSpoolOpenEnforcesCap(t *testing.T) {
	dir := t.TempDir()
	sp, err := OpenSpool(dir, 1<<30, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	sp.segBytes = 300 // ~one batch per segment
	bigName := strings.Repeat("n", 120)
	mk := func(c rune) spoolBatch {
		return spoolBatch{Records: []RecordWire{{InfoHash: strings.Repeat(string(c), 40), Name: bigName}}}
	}
	for _, c := range []rune{'a', 'b', 'c'} {
		if err := sp.Append(mk(c)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	segs, _, _ := sp.Depth()
	if segs != 3 {
		t.Fatalf("segments before reopen = %d, want 3", segs)
	}

	sp2, err := OpenSpool(dir, 620, testLogger()) // ~two segments fit
	if err != nil {
		t.Fatal(err)
	}
	segs, _, recs := sp2.Depth()
	if segs != 2 || recs != 2 {
		t.Fatalf("after reopen: %d segs / %d records, want 2/2 (oldest dropped at open)", segs, recs)
	}
	if sp2.DroppedRecords() != 1 {
		t.Fatalf("DroppedRecords = %d, want 1", sp2.DroppedRecords())
	}
}
