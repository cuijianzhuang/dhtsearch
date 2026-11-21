package nodepush

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/scraper"
)

// Pusher tuning. The caps are deliberately modest: a worker that
// outruns its center by more than this is not helping anyone. What
// the memory buffer cannot hold spills to the disk spool when one
// is enabled (see EnableSpool); only with no spool — or when the
// spool itself is full — is data dropped, and always counted.
const (
	defaultFlushRecords  = 100              // flush when this many records are buffered
	defaultFlushInterval = 5 * time.Second  // ... or this long since the last flush
	defaultBufferRecords = 2000             // buffered-record cap; oldest drop past it
	defaultBufferSwarm   = 8192             // buffered swarm-observation cap
	pushTimeout          = 30 * time.Second // one ingest call's budget (matches the client)
	retryWait            = time.Second      // pause between a failed flush and its one retry
)

// Pusher is the worker's record sink: the fetch pipeline hands it
// every fetched record (through Push, from the fetcher's onRecord)
// and the scraper hands it every scrape round (through PushSwarm);
// a background loop batches both into ingest calls to the center.
//
// Push and PushSwarm never block on the network — they are called
// synchronously from fetch workers and the scraper's sink path.
// A full buffer spills its OLDEST entries to the disk spool when
// one is enabled; with no spool it drops them: fresh records
// describe swarms as they are now, and when the center is
// unreachable the newest evidence is worth more than a backlog of
// stale fetches. (A spill is one local append+fsync — bounded,
// unlike a network round trip.)
type Pusher struct {
	client *Client
	logger *log.Logger

	flushRecords  int
	flushInterval time.Duration
	bufferRecords int
	bufferSwarm   int

	// spool, when non-nil, is the disk fallback for undelivered
	// batches. Set once by EnableSpool before Run starts.
	spool *Spool

	mu      sync.Mutex
	records []queuedRecord
	swarm   []SwarmWire
	notify  chan struct{}

	pushed  atomic.Int64 // records the center accepted (its ingest summary)
	dropped atomic.Int64 // records dropped from a full buffer or a dead-lettered batch
}

// queuedRecord is one buffered record plus the feeds alias the
// worker held for it at fetch time.
type queuedRecord struct {
	rec   metadata.Record
	alias string
}

// NewPusher builds a pusher around client. Zero-valued tuning
// fields take the defaults above.
func NewPusher(client *Client, logger *log.Logger) *Pusher {
	if logger == nil {
		logger = log.Default()
	}
	return &Pusher{
		client:        client,
		logger:        logger,
		flushRecords:  defaultFlushRecords,
		flushInterval: defaultFlushInterval,
		bufferRecords: defaultBufferRecords,
		bufferSwarm:   defaultBufferSwarm,
		notify:        make(chan struct{}, 1),
	}
}

// EnableSpool turns on the disk spool in dir (created if needed),
// bounded to maxBytes in total. Existing segments from a previous
// run are picked up and replayed by Run before new traffic is
// flushed. Call before Run; on error the pusher stays memory-only.
func (p *Pusher) EnableSpool(dir string, maxBytes int64) error {
	sp, err := OpenSpool(dir, maxBytes, p.logger)
	if err != nil {
		return err
	}
	p.spool = sp
	if segs, bytes, recs := sp.Depth(); segs > 0 {
		p.logger.Printf("nodepush: spool at %s holds %d segments, %d bytes, ~%d records from a previous run — replaying", dir, segs, bytes, recs)
	}
	return nil
}

// Push buffers one fetched record for the next ingest batch. It is
// the worker-mode replacement for the local filter+store sink: the
// record is NOT filtered or stored here — that is the center's job,
// so the two deployments cannot drift apart on policy.
func (p *Pusher) Push(rec metadata.Record, alias string) {
	p.mu.Lock()
	p.records = append(p.records, queuedRecord{rec: rec, alias: alias})
	var spill []queuedRecord
	if over := len(p.records) - p.bufferRecords; over > 0 {
		if p.spool != nil {
			// Spill a full batch's worth of the oldest records,
			// not just the overflow: during an outage the buffer
			// sits at its cap and spilling one record per Push
			// would mean one fsync per record.
			n := min(len(p.records)-1, maxIngestRecords)
			spill = make([]queuedRecord, n)
			copy(spill, p.records[:n])
			p.records = append(p.records[:0], p.records[n:]...)
		} else {
			p.records = append(p.records[:0], p.records[over:]...)
			p.dropped.Add(int64(over))
		}
	}
	full := len(p.records) >= p.flushRecords
	p.mu.Unlock()
	if spill != nil {
		p.spoolRecords(spill, nil)
	}
	if full {
		p.signal()
	}
}

// PushSwarm buffers one scrape round's aggregates. It matches the
// scraper's Sink signature, so the worker wires it where a
// standalone node wires its store sink.
func (p *Pusher) PushSwarm(stats []scraper.SwarmStat) {
	if len(stats) == 0 {
		return
	}
	p.mu.Lock()
	for _, sw := range stats {
		p.swarm = append(p.swarm, SwarmWireFrom(sw))
	}
	var spill []SwarmWire
	if over := len(p.swarm) - p.bufferSwarm; over > 0 {
		if p.spool != nil {
			n := min(len(p.swarm), maxIngestSwarm)
			spill = make([]SwarmWire, n)
			copy(spill, p.swarm[:n])
			p.swarm = append(p.swarm[:0], p.swarm[n:]...)
		} else {
			p.swarm = append(p.swarm[:0], p.swarm[over:]...)
		}
	}
	p.mu.Unlock()
	if spill != nil {
		p.spoolRecords(nil, spill)
	}
}

// spoolRecords appends one batch (records and/or swarm) to the
// disk spool. A failed append falls back to the pre-spool
// semantics: the records are counted as dropped. Swarm-only losses
// are not counted — swarm observations carry no drop counter
// anywhere in the pipeline (the memory path drops them silently
// too).
func (p *Pusher) spoolRecords(recs []queuedRecord, swarm []SwarmWire) {
	b := spoolBatch{Swarm: swarm}
	for _, qr := range recs {
		b.Records = append(b.Records, RecordWireFrom(qr.rec, qr.alias))
	}
	if err := p.spool.Append(b); err != nil {
		p.logger.Printf("nodepush: spool append failed, dropping %d records: %v", len(b.Records), err)
		p.dropped.Add(int64(len(b.Records)))
	}
}

// Pushed returns how many records the center has accepted since
// start (the ingest summaries' ingested+filtered counts: from the
// worker's side both mean "delivered"). The server's stats mirror
// and stall watchdog read it as the worker's ingest-progress
// signal, the role the stats table's "fetched" counter plays on a
// standalone node.
func (p *Pusher) Pushed() int64 { return p.pushed.Load() }

// Dropped returns how many buffered records were dropped since
// start: memory-buffer overflow with no spool, a spool append that
// failed, or spool segments the size cap forced out. (With a spool
// enabled, an ordinary undelivered batch is NOT dropped — it is on
// disk, counted by Spooled instead.)
func (p *Pusher) Dropped() int64 {
	n := p.dropped.Load()
	if p.spool != nil {
		n += p.spool.DroppedRecords()
	}
	return n
}

// Spooled returns how many records have been written to the disk
// spool since start (0 when no spool is enabled).
func (p *Pusher) Spooled() int64 {
	if p.spool == nil {
		return 0
	}
	return p.spool.Spooled()
}

// Replayed returns how many spooled records the center has ACKed
// through replay since start (0 when no spool is enabled).
func (p *Pusher) Replayed() int64 {
	if p.spool == nil {
		return 0
	}
	return p.spool.Replayed()
}

// SpoolDepth reports the spool's current occupancy; ok is false
// when no spool is enabled.
func (p *Pusher) SpoolDepth() (segments int, bytes int64, records int64, ok bool) {
	if p.spool == nil {
		return 0, 0, 0, false
	}
	segments, bytes, records = p.spool.Depth()
	return segments, bytes, records, true
}

// Buffered returns the current buffer depths (records, swarm).
func (p *Pusher) Buffered() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.records), len(p.swarm)
}

func (p *Pusher) signal() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// Run flushes until ctx ends, then makes one final bounded attempt
// so a clean shutdown hands over what it already fetched. A spool
// left over from a previous run is replayed first; whatever a clean
// shutdown still cannot deliver is spooled for the next start (or,
// without a spool, dropped and counted).
func (p *Pusher) Run(ctx context.Context) {
	// Startup replay: batches a previous run spooled are the
	// oldest undelivered data — try them before the ticker's
	// first round mixes them with new traffic. (Strict ordering
	// is not required: new records may pass spooled ones, and the
	// center's upserts are idempotent either way.)
	p.flush(ctx)
	t := time.NewTicker(p.flushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
			p.flush(fctx)
			cancel()
			if n, _ := p.Buffered(); n > 0 {
				if p.spool != nil {
					p.spillBuffer()
					if m, _ := p.Buffered(); m == 0 {
						p.logger.Printf("nodepush: shutdown with %d records undelivered — spooled to disk for the next start", n)
					}
				} else {
					p.logger.Printf("nodepush: shutdown with %d records undelivered (center unreachable or slow); they are dropped — no disk spool enabled", n)
				}
			}
			if p.spool != nil {
				_ = p.spool.Close()
			}
			return
		case <-t.C:
			p.flush(ctx)
		case <-p.notify:
			p.flush(ctx)
		}
	}
}

// spillBuffer moves everything still buffered in memory into the
// spool, in protocol-sized batches. Used at shutdown, where the
// alternative is dropping it.
func (p *Pusher) spillBuffer() {
	for {
		p.mu.Lock()
		if len(p.records) == 0 && len(p.swarm) == 0 {
			p.mu.Unlock()
			return
		}
		n := min(len(p.records), maxIngestRecords)
		recs := make([]queuedRecord, n)
		copy(recs, p.records[:n])
		p.records = append(p.records[:0], p.records[n:]...)
		swarm := p.swarm
		p.swarm = nil
		p.mu.Unlock()
		p.spoolRecords(recs, swarm)
	}
}

// flush first replays the disk spool (oldest data first), then
// sends memory batches (all buffered records up to a sane batch
// size, plus all buffered swarm rows) with one retry each. A batch
// that fails twice is spooled when a spool is enabled; without one
// it goes back to the FRONT of the buffer — it is the oldest data —
// and the buffer cap then decides what survives.
func (p *Pusher) flush(ctx context.Context) {
	if p.spool != nil && !p.spool.Empty() {
		err := p.spool.Replay(ctx, func(ctx context.Context, b spoolBatch) (*IngestResponse, error) {
			resp, err := p.ingestWithRetry(ctx, IngestRequest{Records: b.Records, Swarm: b.Swarm})
			if err == nil {
				p.pushed.Add(int64(resp.Ingested + resp.Filtered))
			}
			return resp, err
		})
		if err != nil {
			// The center is still (or again) unreachable: stop
			// here rather than piling a memory batch onto the
			// same failure — it is spooled by the next round if
			// the outage lasts, and replay resumes where it
			// stopped. Logged once per flush round, like the
			// memory path's failure line.
			p.logger.Printf("nodepush: spool replay paused (center unreachable): %v", err)
			return
		}
	}
	for {
		p.mu.Lock()
		if len(p.records) == 0 && len(p.swarm) == 0 {
			p.mu.Unlock()
			return
		}
		n := min(len(p.records), maxIngestRecords)
		recs := make([]queuedRecord, n)
		copy(recs, p.records[:n])
		p.records = append(p.records[:0], p.records[n:]...)
		swarm := p.swarm
		p.swarm = nil
		p.mu.Unlock()

		req := IngestRequest{Swarm: swarm}
		for _, qr := range recs {
			req.Records = append(req.Records, RecordWireFrom(qr.rec, qr.alias))
		}
		resp, err := p.ingestWithRetry(ctx, req)
		if err != nil {
			if p.spool != nil {
				// Retain the batch on disk instead of bouncing
				// it around the memory cap for the whole outage.
				p.spoolRecords(recs, swarm)
				p.logger.Printf("nodepush: ingest to %s failed (batch spooled): %v", p.client.base, err)
				return
			}
			// Dead-letter the batch back onto the buffer front,
			// then let the cap trim the oldest — which, after a
			// long outage, is this very batch.
			p.mu.Lock()
			p.records = append(recs, p.records...)
			if over := len(p.records) - p.bufferRecords; over > 0 {
				p.records = append(p.records[:0], p.records[over:]...)
				p.dropped.Add(int64(over))
			}
			p.swarm = append(swarm, p.swarm...)
			if over := len(p.swarm) - p.bufferSwarm; over > 0 {
				p.swarm = append(p.swarm[:0], p.swarm[over:]...)
			}
			p.mu.Unlock()
			p.logger.Printf("nodepush: ingest to %s failed (batch requeued): %v", p.client.base, err)
			return
		}
		p.pushed.Add(int64(resp.Ingested + resp.Filtered))
		// Loop: more than one batch may be buffered after an outage.
	}
}

// ingestWithRetry performs the ingest call with exactly one retry,
// as the worker's contract with a flaky network path specifies.
func (p *Pusher) ingestWithRetry(ctx context.Context, req IngestRequest) (*IngestResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, pushTimeout)
	resp, err := p.client.Ingest(cctx, req)
	cancel()
	if err == nil {
		return resp, nil
	}
	select {
	case <-ctx.Done():
		return nil, err
	case <-time.After(retryWait):
	}
	cctx, cancel = context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	return p.client.Ingest(cctx, req)
}

// maxIngestRecords and maxIngestSwarm are the protocol's per-batch
// caps; the center rejects larger batches with 413, so the pusher
// never builds one (in memory or on disk).
const (
	maxIngestRecords = 500
	maxIngestSwarm   = 5000
)
