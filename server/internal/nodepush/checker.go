package nodepush

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"dhtsearch/server/internal/admission"
	"dhtsearch/server/internal/metadata"
)

// checkMaxHashes is the protocol's per-call hash cap; the center
// rejects larger check calls, so the checker chunks.
const checkMaxHashes = 500

// checkTimeout bounds one check call. The checker runs in the
// pipeline's bridge goroutines; a slow center must degrade to
// fail-open quickly rather than back the bridges up.
const checkTimeout = 10 * time.Second

// Checker is the worker-side admission gate: it asks the center
// which of a batch of discovered hashes are already stored or in
// failure backoff there, and applies the SAME decision rules as
// the standalone gate (internal/admission) to the answer — stored
// refuses outright; backoff refuses unless the item carries fresh
// evidence (admission.BackoffExempt). A failed check admits
// everything in the batch (fail-open), exactly like a store error
// in the local gate: admission is an optimization, and a center
// hiccup must not stall the worker's ingest.
type Checker struct {
	client *Client
	logger *log.Logger

	// EnabledFunc, when non-nil, is consulted on every batch, so
	// the worker's negative-cache toggle (META_NEG_CACHE_ENABLED,
	// read from its own local settings) applies live, mirroring
	// admission.Gate. Nil means always enabled.
	EnabledFunc func() bool

	skippedStored  atomic.Int64
	skippedBackoff atomic.Int64
}

// NewChecker builds a checker around client.
func NewChecker(client *Client, logger *log.Logger) *Checker {
	if logger == nil {
		logger = log.Default()
	}
	return &Checker{client: client, logger: logger}
}

func (c *Checker) enabled() bool {
	return c.EnabledFunc == nil || c.EnabledFunc()
}

// AdmitBatch returns the subset of items the center's state admits,
// in input order. It makes one check call per checkMaxHashes items.
func (c *Checker) AdmitBatch(items []metadata.Item) []metadata.Item {
	if !c.enabled() || len(items) == 0 {
		return items
	}
	out := items[:0]
	// Work on a copy of the slice header only: out shares the
	// backing array with items, which is safe because admitted
	// items only ever move toward the front.
	for start := 0; start < len(items); start += checkMaxHashes {
		end := min(start+checkMaxHashes, len(items))
		chunk := items[start:end]
		hashes := make([]string, 0, len(chunk))
		for _, it := range chunk {
			if it.Hash != "" {
				hashes = append(hashes, it.Hash)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		stored, backoff, err := c.client.Check(ctx, hashes)
		cancel()
		if err != nil {
			c.logger.Printf("nodepush: admission check failed (admitting batch): %v", err)
			out = append(out, chunk...)
			continue
		}
		for _, it := range chunk {
			switch {
			case it.Hash == "":
				out = append(out, it)
			case stored[it.Hash]:
				c.skippedStored.Add(1)
			case backoff[it.Hash] && !admission.BackoffExempt(it):
				c.skippedBackoff.Add(1)
			default:
				out = append(out, it)
			}
		}
	}
	return out
}

// Skipped returns the cumulative refusal counts since process
// start, mirroring admission.Gate.Skipped so the server's stats
// mirror treats both gates identically.
func (c *Checker) Skipped() (stored, backoff int64) {
	return c.skippedStored.Load(), c.skippedBackoff.Load()
}

// GateChannel adapts the batch checker to the pipeline bridges'
// channel shape: items arrive one by one, are collected into
// batches (gateBatchSize items or gateWindow, whichever first),
// checked in one call per batch, and the admitted ones flow on.
// The window keeps a slow trickle from waiting for a full batch;
// the size cap keeps a burst from building latency. On shutdown
// (ctx done or input closed) a partial batch is still checked and
// emitted before the output closes, unless ctx is done.
func (c *Checker) GateChannel(ctx context.Context, in <-chan metadata.Item) <-chan metadata.Item {
	const (
		gateBatchSize = 128
		gateWindow    = 100 * time.Millisecond
	)
	out := make(chan metadata.Item)
	go func() {
		defer close(out)
		var batch []metadata.Item
		timer := time.NewTimer(gateWindow)
		if !timer.Stop() {
			<-timer.C
		}
		// flush checks the pending batch and emits the admitted
		// items; it reports false when ctx ended mid-emission.
		flush := func() bool {
			if len(batch) == 0 {
				return true
			}
			admitted := c.AdmitBatch(batch)
			batch = nil
			for _, it := range admitted {
				select {
				case out <- it:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		for {
			select {
			case <-ctx.Done():
				return
			case it, ok := <-in:
				if !ok {
					flush()
					return
				}
				if len(batch) == 0 {
					timer.Reset(gateWindow)
				}
				batch = append(batch, it)
				if len(batch) >= gateBatchSize {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					if !flush() {
						return
					}
				}
			case <-timer.C:
				if !flush() {
					return
				}
			}
		}
	}()
	return out
}
