package nodepush

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// The disk spool is the pusher's fallback for center outages: when
// a batch cannot be delivered and the in-memory buffer cannot hold
// it either, the batch is appended to a segment file under
// <data dir>/spool/ and replayed once the center answers again. It
// exists so a worker keeps what it fetched across a center outage
// or its own restart — up to a hard size cap, past which the oldest
// segments are dropped (and counted). It is deliberately NOT a
// general message queue: one producer, one consumer, no ordering
// guarantee beyond oldest-segment-first replay (the center's
// upserts are idempotent, so a replayed batch is harmless).

// spoolBatch is the on-disk batch unit: exactly what one ingest
// call carries, minus the node name (the client stamps that at send
// time). Keeping the disk unit identical to the wire unit makes
// replay literally "send this batch again".
type spoolBatch struct {
	Records []RecordWire `json:"records"`
	Swarm   []SwarmWire  `json:"swarm"`
}

// Spool tuning defaults. The segment size bounds how much data one
// torn write can strand and how coarse the cap accounting is; the
// total cap bounds what a long center outage may cost in disk.
const (
	defaultSpoolMaxBytes = 1 << 30 // NODE_SPOOL_MAX_MB default (1024)
	defaultSegmentBytes  = 8 << 20 // rotate a segment at ~8MB
	maxFrameBytes        = 64 << 20
)

// segmentMeta is the in-memory bookkeeping for one segment file.
// size and records describe the VALID prefix only: a corrupt tail
// is truncated away at open, so what is counted here is exactly
// what replay will read.
type segmentMeta struct {
	seq     int
	path    string
	size    int64
	records int64
	batches int
}

// ackProgress remembers, per segment, how many of its frames (and
// the records in them) the center has ACKed during this process's
// replay. It is memory-only on purpose: after a crash the segment
// is replayed from its start and the center's idempotent upserts
// absorb the duplicates.
type ackProgress struct {
	frames  int
	records int64
}

// Spool is an append-only store of undelivered ingest batches.
// All methods are safe for concurrent use; the network sends in
// Replay happen WITHOUT the mutex held, so appends never wait on
// the center.
type Spool struct {
	mu       sync.Mutex
	dir      string
	logger   *log.Logger
	maxBytes int64
	segBytes int64

	segs     []*segmentMeta // oldest first; appends target the last one
	nextSeq  int
	cur      *os.File // append handle for the newest segment, if any
	curSeq   int
	total    int64 // sum of segment sizes (valid bytes)
	acked    map[int]ackProgress
	dropping bool // inside a cap-drop episode (log once per episode)

	spooled  atomic.Int64 // records ever appended
	replayed atomic.Int64 // records in batches the center ACKed via replay
	dropped  atomic.Int64 // records lost to cap drops (feeds Pusher.Dropped)
	corrupt  atomic.Int64 // corrupt/torn frame incidents found at open
	truncB   atomic.Int64 // bytes truncated away at open
}

// OpenSpool creates (if needed) and scans the spool directory. Each
// segment's frames are validated; a torn or corrupt tail — the
// signature of a crash mid-append — is truncated and counted, never
// fatal. A frame that fails validation mid-segment ends that
// segment there: frames after it are unreachable by construction
// and are truncated with it.
func OpenSpool(dir string, maxBytes int64, logger *log.Logger) (*Spool, error) {
	if logger == nil {
		logger = log.Default()
	}
	if maxBytes <= 0 {
		maxBytes = defaultSpoolMaxBytes
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Spool{
		dir:      dir,
		logger:   logger,
		maxBytes: maxBytes,
		segBytes: defaultSegmentBytes,
		acked:    make(map[int]ackProgress),
		nextSeq:  1,
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var seqs []int
	for _, e := range entries {
		if seq, ok := parseSegmentName(e.Name()); ok {
			seqs = append(seqs, seq)
		}
	}
	sort.Ints(seqs)
	for _, seq := range seqs {
		path := filepath.Join(dir, segmentName(seq))
		meta, err := s.scanSegment(seq, path)
		if err != nil {
			return nil, err
		}
		if meta.batches == 0 {
			// Nothing valid left (fully corrupt, or an empty file
			// from a crash between create and first write): drop
			// the husk rather than replaying an empty segment.
			_ = os.Remove(path)
			continue
		}
		s.segs = append(s.segs, meta)
		s.total += meta.size
		if seq >= s.nextSeq {
			s.nextSeq = seq + 1
		}
	}
	// Enforce the size cap on what was found, not just on future
	// appends: the cap may have been lowered since the segments
	// were written (or files added out of band), and without this
	// pass the spool would sit over its configured bound until the
	// first Append of the new run.
	s.enforceCapLocked()
	return s, nil
}

func segmentName(seq int) string { return fmt.Sprintf("seg-%06d.spool", seq) }

func parseSegmentName(name string) (int, bool) {
	if !strings.HasPrefix(name, "seg-") || !strings.HasSuffix(name, ".spool") {
		return 0, false
	}
	var seq int
	if _, err := fmt.Sscanf(name, "seg-%d.spool", &seq); err != nil || seq <= 0 {
		return 0, false
	}
	return seq, true
}

// scanSegment reads one segment file, decodes its valid frame
// prefix and truncates anything past it (counting the loss).
func (s *Spool) scanSegment(seq int, path string) (*segmentMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	frames, valid := parseFrames(data)
	if valid < len(data) {
		s.corrupt.Add(1)
		s.truncB.Add(int64(len(data) - valid))
		s.logger.Printf("nodepush: spool segment %s: truncated %d corrupt/torn bytes at offset %d", filepath.Base(path), len(data)-valid, valid)
		if err := os.Truncate(path, int64(valid)); err != nil {
			return nil, err
		}
	}
	m := &segmentMeta{seq: seq, path: path, size: int64(valid), batches: len(frames)}
	for _, b := range frames {
		m.records += int64(len(b.Records))
	}
	return m, nil
}

// parseFrames decodes the valid frame prefix of buf: a frame is a
// 4-byte big-endian length followed by that many bytes of JSON.
// It stops at the first incomplete or undecodable frame and
// returns the batches plus the offset just past the last good one.
func parseFrames(buf []byte) ([]spoolBatch, int) {
	var out []spoolBatch
	off := 0
	for {
		if len(buf)-off < 4 {
			break
		}
		n := int(binary.BigEndian.Uint32(buf[off:]))
		if n <= 0 || n > maxFrameBytes || len(buf)-off-4 < n {
			break
		}
		var b spoolBatch
		if err := json.Unmarshal(buf[off+4:off+4+n], &b); err != nil {
			break
		}
		out = append(out, b)
		off += 4 + n
	}
	return out, off
}

// readSegment loads one segment's valid batches from disk (used at
// replay time; the file was already validated/truncated at open,
// but a frame that no longer parses simply ends the segment, the
// same rule as at open).
func readSegment(path string) ([]spoolBatch, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	frames, _ := parseFrames(data)
	return frames, nil
}

// Append writes one batch as a new frame in the current segment
// (rotating at the segment size), fsyncs, then enforces the total
// cap by dropping the oldest sealed segments. An append error
// leaves the spool unchanged in every way the caller can observe:
// the batch is still the caller's problem.
func (s *Spool) Append(b spoolBatch) error {
	payload, err := json.Marshal(b)
	if err != nil {
		return err
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureCurrentLocked(int64(len(frame))); err != nil {
		return err
	}
	if _, err := s.cur.Write(frame); err != nil {
		return err
	}
	if err := s.cur.Sync(); err != nil {
		return err
	}
	m := s.segs[len(s.segs)-1]
	m.size += int64(len(frame))
	m.batches++
	m.records += int64(len(b.Records))
	s.total += int64(len(frame))
	s.spooled.Add(int64(len(b.Records)))
	s.enforceCapLocked()
	return nil
}

// ensureCurrentLocked makes sure an append handle for the newest
// segment exists and has room for a frame of the given size,
// rotating to a fresh segment when the current one is at the size
// threshold. A single oversized frame still lands (in a fresh
// segment) — rotation is a soft target, never a rejection.
func (s *Spool) ensureCurrentLocked(frameLen int64) error {
	if s.cur != nil {
		cur := s.segs[len(s.segs)-1]
		if cur.size == 0 || cur.size+frameLen <= s.segBytes {
			return nil
		}
		_ = s.cur.Close()
		s.cur = nil
	}
	seq := s.nextSeq
	s.nextSeq++
	path := filepath.Join(s.dir, segmentName(seq))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	s.segs = append(s.segs, &segmentMeta{seq: seq, path: path})
	s.cur = f
	s.curSeq = seq
	return nil
}

// enforceCapLocked drops oldest segments until the total size fits
// the cap. The segment currently being appended to is never
// dropped; if it alone exceeds the cap the spool stays over — the
// cap is enforced at segment granularity, so the overshoot is
// bounded by one segment plus one frame. Dropped records feed the
// pusher's drop counter; the drop is logged once per episode.
func (s *Spool) enforceCapLocked() {
	droppedNow := false
	for s.total > s.maxBytes && len(s.segs) > 1 {
		victim := s.segs[0]
		lost := victim.records - s.acked[victim.seq].records
		s.removeSegmentLocked(0)
		if lost > 0 {
			s.dropped.Add(lost)
		}
		droppedNow = true
	}
	if droppedNow && !s.dropping {
		s.dropping = true
		s.logger.Printf("nodepush: spool over its %d-byte cap — oldest segments dropped (newest data kept); dropped records are counted in node_dropped", s.maxBytes)
	}
	if s.total <= s.maxBytes {
		s.dropping = false
	}
}

// removeSegmentLocked deletes the segment at index i from disk and
// from the bookkeeping. Caller holds s.mu.
func (s *Spool) removeSegmentLocked(i int) {
	m := s.segs[i]
	if s.cur != nil && m.seq == s.curSeq {
		_ = s.cur.Close()
		s.cur = nil
	}
	_ = os.Remove(m.path)
	s.total -= m.size
	delete(s.acked, m.seq)
	s.segs = append(s.segs[:i], s.segs[i+1:]...)
}

// sendFunc delivers one spooled batch and returns the center's
// summary; a nil error means the center ACKed the batch.
type sendFunc func(ctx context.Context, b spoolBatch) (*IngestResponse, error)

// Replay delivers every spooled batch, oldest segment first,
// through send (the pusher's normal ingest-with-retry path),
// stopping at the first failure — the failed batch and everything
// behind it stay for the next round, driven by the pusher's flush
// ticker rather than a hot loop here. A segment file is deleted
// only once every batch in it has been ACKed. Batches ACKed before
// a mid-segment failure are remembered (in memory) and not resent
// this session; after a process restart the segment replays from
// its start and the center's idempotent upserts absorb any repeat.
func (s *Spool) Replay(ctx context.Context, send sendFunc) error {
	for {
		s.mu.Lock()
		if len(s.segs) == 0 {
			s.mu.Unlock()
			return nil
		}
		seg := s.segs[0]
		start := s.acked[seg.seq].frames
		s.mu.Unlock()

		frames, err := readSegment(seg.path)
		if err != nil {
			return err
		}
		if start > len(frames) {
			start = len(frames)
		}
		for i := start; i < len(frames); i++ {
			_, err := send(ctx, frames[i])
			if err != nil {
				s.mu.Lock()
				p := s.acked[seg.seq]
				if i > p.frames {
					p.frames = i
				}
				s.acked[seg.seq] = p
				s.mu.Unlock()
				return err
			}
			s.mu.Lock()
			p := s.acked[seg.seq]
			p.frames = i + 1
			p.records += int64(len(frames[i].Records))
			s.acked[seg.seq] = p
			s.mu.Unlock()
			s.replayed.Add(int64(len(frames[i].Records)))
		}
		// The whole snapshot was ACKed. Delete the segment only
		// if nothing was appended to it in the meantime (appends
		// target the newest segment — which this may be).
		s.mu.Lock()
		if len(s.segs) > 0 && s.segs[0].seq == seg.seq && s.segs[0].batches <= len(frames) {
			s.removeSegmentLocked(0)
			s.mu.Unlock()
			continue
		}
		s.mu.Unlock()
		return nil
	}
}

// Depth reports the spool's current occupancy: segment count,
// total valid bytes and records still awaiting delivery (records
// in ACKed frames of a not-yet-deleted segment are excluded).
func (s *Spool) Depth() (segments int, bytes int64, records int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.segs {
		records += m.records - s.acked[m.seq].records
	}
	return len(s.segs), s.total, records
}

// Empty reports whether any segment is on disk.
func (s *Spool) Empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.segs) == 0
}

// Spooled returns how many records have ever been appended.
func (s *Spool) Spooled() int64 { return s.spooled.Load() }

// Replayed returns how many records the center has ACKed through
// replay.
func (s *Spool) Replayed() int64 { return s.replayed.Load() }

// DroppedRecords returns how many records cap enforcement has
// dropped. The pusher folds this into its Dropped counter.
func (s *Spool) DroppedRecords() int64 { return s.dropped.Load() }

// Corrupt returns how many corrupt/torn frame incidents open found
// (and truncated away).
func (s *Spool) Corrupt() int64 { return s.corrupt.Load() }

// Close releases the append handle. Segment files stay on disk for
// the next start.
func (s *Spool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		err := s.cur.Close()
		s.cur = nil
		return err
	}
	return nil
}
