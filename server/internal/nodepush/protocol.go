// Package nodepush is the crawler worker node's client half of the
// node protocol: a worker node (a dhtsearch server started with
// CENTER_URL set) discovers, scrapes and fetches exactly like a
// standalone node, but instead of filtering and storing records
// locally it pushes them to its center node, which owns the index,
// the content filter and all policy. This package carries the wire
// contract (shared with the center's handlers in internal/api), the
// HTTP client, the batching record pusher and the admission checker
// that asks the center which hashes are worth a fetch.
//
// The protocol is deliberately small — two calls:
//
//   - POST /api/node/check {hashes} → {stored, backoff}: the
//     center's admission state for a batch of hashes, so the worker
//     spends fetch slots exactly where a standalone node's gate
//     would (see internal/admission).
//   - POST /api/node/ingest {node, records, swarm}: one batch of
//     fetched records plus the worker's tracker-scrape observations
//     since the last batch. The center filters, stores and counts
//     them as if it had fetched them itself.
//
// Authentication is one shared secret (NODE_TOKEN) presented as a
// bearer token — the same shape as the deployment's other machine
// credentials. The endpoints are internal: they are not part of the
// public API and are never documented on it.
package nodepush

import (
	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/scraper"
)

// RecordWire is one fetched torrent record as it travels from a
// worker to the center. The field names mirror store.Torrent's JSON
// shape: the center rebuilds a store.Torrent from this and upserts
// it, so the stored row (including the zstd files blob) is identical
// to one a local fetch would have produced. Source is the record's
// discovery-source label (metadata.Source.Name) so the center's
// per-source counters attribute the fetch to the channel that found
// it, not to the node that carried it. Alias is the worker-side feed
// title for the hash, when its feeds service knows one.
type RecordWire struct {
	InfoHash  string        `json:"info_hash"`
	Name      string        `json:"name"`
	TotalSize int64         `json:"total_size"`
	FileCount int           `json:"file_count"`
	Files     []filter.File `json:"files"`
	Source    string        `json:"source"`
	Alias     string        `json:"alias,omitempty"`
}

// RecordWireFrom converts a fetched record (plus the feeds alias
// the worker holds for it, "" when none) to its wire form.
func RecordWireFrom(rec metadata.Record, alias string) RecordWire {
	return RecordWire{
		InfoHash:  rec.InfoHash,
		Name:      rec.Name,
		TotalSize: rec.TotalSize,
		FileCount: rec.FileCount,
		Files:     rec.Files,
		Source:    rec.Source.Name(),
		Alias:     alias,
	}
}

// Record converts the wire form back to a metadata.Record, folding
// an unknown source label into SourceDHT like every other source
// consumer.
func (w RecordWire) Record() metadata.Record {
	return metadata.Record{
		InfoHash:  w.InfoHash,
		Name:      w.Name,
		TotalSize: w.TotalSize,
		FileCount: w.FileCount,
		Files:     w.Files,
		Source:    metadata.ParseSource(w.Source),
	}
}

// SwarmWire is one tracker-scrape observation as it travels: the
// same per-field-max aggregate the local scrape sink persists
// (scraper.SwarmStat), keyed by hash. The center stamps the receipt
// time as the snapshot time, exactly as its own sink does for local
// rounds.
type SwarmWire struct {
	InfoHash  string `json:"info_hash"`
	Seeders   int64  `json:"seeders"`
	Leechers  int64  `json:"leechers"`
	Completed int64  `json:"completed"`
}

// SwarmWireFrom converts one scraper aggregate to its wire form.
func SwarmWireFrom(sw scraper.SwarmStat) SwarmWire {
	return SwarmWire{
		InfoHash:  sw.Hash,
		Seeders:   int64(sw.Seeders),
		Leechers:  int64(sw.Leechers),
		Completed: int64(sw.Completed),
	}
}

// CheckRequest asks the center for the admission state of a batch
// of hashes.
type CheckRequest struct {
	Hashes []string `json:"hashes"`
}

// CheckResponse is the center's admission state: Stored hashes are
// already in the center's index, Backoff hashes have an unexpired
// failure backoff there. A hash in neither list is worth a fetch.
// (A hash never appears in both: stored is checked first, exactly
// like the local gate.)
type CheckResponse struct {
	Stored  []string `json:"stored"`
	Backoff []string `json:"backoff"`
}

// IngestRequest is one push batch: the pushing node's name, the
// records it fetched since the last batch, and the swarm
// observations its scraper made in the same window.
type IngestRequest struct {
	Node    string       `json:"node"`
	Records []RecordWire `json:"records"`
	Swarm   []SwarmWire  `json:"swarm"`
}

// IngestResponse summarizes what the center did with a batch:
// records stored, records its filter dropped, swarm rows merged.
type IngestResponse struct {
	Ingested int `json:"ingested"`
	Filtered int `json:"filtered"`
	Swarm    int `json:"swarm"`
}
