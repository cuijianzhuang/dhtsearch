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
// outruns its center by more than this is not helping anyone, and
// v1 keeps no disk spool — what the buffer cannot hold is dropped
// (and counted), never allowed to grow memory without bound.
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
// Push and PushSwarm never block — they are called synchronously
// from fetch workers and the scraper's sink path. A full buffer
// drops the OLDEST entries: fresh records describe swarms as they
// are now, and when the center is unreachable the newest evidence
// is worth more than a backlog of stale fetches.
type Pusher struct {
	client *Client
	logger *log.Logger

	flushRecords  int
	flushInterval time.Duration
	bufferRecords int
	bufferSwarm   int

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

// Push buffers one fetched record for the next ingest batch. It is
// the worker-mode replacement for the local filter+store sink: the
// record is NOT filtered or stored here — that is the center's job,
// so the two deployments cannot drift apart on policy.
func (p *Pusher) Push(rec metadata.Record, alias string) {
	p.mu.Lock()
	p.records = append(p.records, queuedRecord{rec: rec, alias: alias})
	if over := len(p.records) - p.bufferRecords; over > 0 {
		p.records = append(p.records[:0], p.records[over:]...)
		p.dropped.Add(int64(over))
	}
	full := len(p.records) >= p.flushRecords
	p.mu.Unlock()
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
	if over := len(p.swarm) - p.bufferSwarm; over > 0 {
		p.swarm = append(p.swarm[:0], p.swarm[over:]...)
	}
	p.mu.Unlock()
}

// Pushed returns how many records the center has accepted since
// start (the ingest summaries' ingested+filtered counts: from the
// worker's side both mean "delivered"). The server's stats mirror
// and stall watchdog read it as the worker's ingest-progress
// signal, the role the stats table's "fetched" counter plays on a
// standalone node.
func (p *Pusher) Pushed() int64 { return p.pushed.Load() }

// Dropped returns how many buffered records were dropped (buffer
// overflow or a batch that failed its retry) since start.
func (p *Pusher) Dropped() int64 { return p.dropped.Load() }

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
// so a clean shutdown hands over what it already fetched.
func (p *Pusher) Run(ctx context.Context) {
	t := time.NewTicker(p.flushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
			p.flush(fctx)
			cancel()
			if n, _ := p.Buffered(); n > 0 {
				p.logger.Printf("nodepush: shutdown with %d records undelivered (center unreachable or slow); they are dropped — worker mode keeps no disk spool", n)
			}
			return
		case <-t.C:
			p.flush(ctx)
		case <-p.notify:
			p.flush(ctx)
		}
	}
}

// flush sends one batch (all buffered records up to a sane batch
// size, plus all buffered swarm rows) with one retry. A batch that
// fails twice goes back to the FRONT of the buffer — it is the
// oldest data — and the buffer cap then decides what survives.
func (p *Pusher) flush(ctx context.Context) {
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

// maxIngestRecords is the protocol's per-batch record cap; the
// center rejects larger batches with 413, so the pusher never
// builds one.
const maxIngestRecords = 500
