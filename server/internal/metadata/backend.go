package metadata

import "context"

// Backend is the contract the pipeline drives a metadata fetcher
// through: jobs in (Items via a channel or the priority Queue),
// Records out, outcome counters for the stats mirror and the stall
// watchdog, and a Close that drains the worker pool. Two foundations
// implement it: Fetcher (anacrolix/torrent clients) and the thin
// purpose-built BEP 9 fetcher in internal/metadata/native, selected by
// the META_FETCHER setting. The counter meanings are part of the
// contract — fetched / timed out / failed / prescreen-dropped must
// mean the same thing on both foundations, or the admin pipeline view
// and the watchdog would compare unlike quantities after a switch.
// So must the per-source attribution (SourceStats): the discovery
// tag rides on the Item, above this contract, and every foundation
// counts each outcome under the item's Source identically.
type Backend interface {
	// Run consumes Items from in until in closes or the backend is
	// closed, calling onRecord for each fetched torrent.
	Run(ctx context.Context, in <-chan Item, onRecord func(Record))
	// RunQueue is Run with the priority Queue as the input.
	RunQueue(ctx context.Context, q *Queue, onRecord func(Record))
	// Stats returns the fetched / timed-out / failed counters.
	Stats() (fetched, timedOut, failed int64)
	// PrescreenDropped returns how many fetches the prescreen phase
	// abandoned early (no peer connection within the prescreen
	// window).
	PrescreenDropped() int64
	// SourceStats returns the per-source fetch counters (attempts
	// and outcomes split by the items' discovery Source), in
	// canonical source order.
	SourceStats() []FetchSourceStats
	// Close stops the workers and releases the foundation's
	// resources.
	Close()
}

// The anacrolix fetcher is the reference implementation of the
// contract.
var _ Backend = (*Fetcher)(nil)
