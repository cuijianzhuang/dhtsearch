// Package admission is the metadata fetch queue's gate: it decides,
// per discovered infohash, whether the hash may spend a fetch worker
// slot at all. Two kinds of waste are refused:
//
//   - hashes already in the torrents table. Nothing upstream checks
//     for them: the crawler's dedup ring is in-memory and bounded,
//     so after an eviction or a restart a stored hash is harvested,
//     scraped, queued and fetched all over again — a full fetch
//     whose upsert then changes nothing (it does still increment
//     the "fetched" counter, which is how the pipeline's derived
//     totals drifted above the library size before PR #41).
//   - hashes in failure backoff. A hash whose metadata fetch failed
//     is overwhelmingly likely to fail again on immediate retry —
//     its swarm is dead or unreachable — yet the DHT re-reports
//     popular dead hashes constantly. Failures are recorded in the
//     store's fetch_attempts table with a doubling backoff (1h, 2h,
//     4h, ... capped), and the gate refuses the hash until the
//     backoff expires.
//
// Fresh evidence overrides backoff, never the stored check: a
// tracker scrape that just reported seeders, and operator-curated
// feed hashes, are admitted even inside a backoff window.
package admission

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"dhtsearch/server/internal/metadata"
)

// Store is the slice of the store the gate needs. *store.Store
// satisfies it; the narrow interface follows the feeds service's
// pattern and keeps the gate testable without a database.
type Store interface {
	FetchAdmission(ctx context.Context, infoHash string) (stored bool, nextRetryAt int64, err error)
	RecordFetchFailure(ctx context.Context, infoHash, outcome string, now time.Time, maxBackoff time.Duration) (int64, error)
	ClearFetchAttempt(ctx context.Context, infoHash string) error
}

// writeTimeout bounds every store call the gate makes, mirroring
// the ingest pipeline's rule (see onRecord in cmd/server): the gate
// runs inside bridge goroutines and fetch workers, and an unbounded
// write over a half-dead connection would park them.
const writeTimeout = 15 * time.Second

// Gate is the admission controller for one fetch pipeline. The zero
// value is not usable; build one with New.
type Gate struct {
	st     Store
	logger *log.Logger

	// EnabledFunc, when non-nil, is consulted on every admission
	// and every outcome recording, so the settings page toggles the
	// negative cache live. Nil means always enabled.
	EnabledFunc func() bool
	// MaxBackoffFunc, when non-nil, supplies the backoff cap used
	// when recording a failure. Nil means the store's default cap.
	MaxBackoffFunc func() time.Duration
	// NowFunc overrides the clock (tests). Nil means time.Now.
	NowFunc func() time.Time

	skippedStored  atomic.Int64
	skippedBackoff atomic.Int64
}

// New builds a gate over st. A nil logger selects log.Default().
func New(st Store, logger *log.Logger) *Gate {
	if logger == nil {
		logger = log.Default()
	}
	return &Gate{st: st, logger: logger}
}

func (g *Gate) enabled() bool {
	return g.EnabledFunc == nil || g.EnabledFunc()
}

func (g *Gate) now() time.Time {
	if g.NowFunc != nil {
		return g.NowFunc()
	}
	return time.Now()
}

// Admit reports whether it may proceed toward the fetch workers.
// Refusals are counted (see Skipped). A store error admits the item:
// the gate is an optimization, and a database hiccup must not stall
// ingest — the failure is logged and the fetch proceeds (its
// outcome recording will surface a persistent store problem too).
func (g *Gate) Admit(it metadata.Item) bool {
	if !g.enabled() || it.Hash == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	stored, nextRetryAt, err := g.st.FetchAdmission(ctx, it.Hash)
	if err != nil {
		g.logger.Printf("admission: lookup %s: %v (admitting)", it.Hash, err)
		return true
	}
	if stored {
		g.skippedStored.Add(1)
		return false
	}
	if nextRetryAt > g.now().Unix() && !backoffExempt(it) {
		g.skippedBackoff.Add(1)
		return false
	}
	return true
}

// BackoffExempt is backoffExempt for the crawler worker node's
// gate (internal/nodepush): it applies this package's exemption
// rules to backoff state reported by the center node, so a worker
// makes exactly the admission decisions a standalone node would.
func BackoffExempt(it metadata.Item) bool {
	return backoffExempt(it)
}

// backoffExempt reports whether an item's source carries evidence
// strong enough to override a failure backoff: a scrape that just
// observed seeders means the swarm was alive moments ago, and a
// feed hash is operator-curated. Neither overrides the stored check
// — that happens before this is consulted.
func backoffExempt(it metadata.Item) bool {
	switch it.Source {
	case metadata.SourceFeed:
		return true
	case metadata.SourceScrape:
		return it.Seeders > 0
	}
	return false
}

// RecordOutcome folds one finished fetch attempt back into the
// negative cache, wired to the fetcher's ResultFunc: a success
// deletes the hash's failure state outright; a failure extends its
// backoff. Called synchronously from fetch workers, so all writes
// are timeout-bounded; errors are logged, never fatal. While the
// cache is disabled it records nothing (existing rows simply age
// out through the periodic prune).
func (g *Gate) RecordOutcome(hash string, outcome metadata.FetchOutcome) {
	if !g.enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	switch outcome {
	case metadata.OutcomeFetched:
		if err := g.st.ClearFetchAttempt(ctx, hash); err != nil {
			g.logger.Printf("admission: clear %s: %v", hash, err)
		}
	case metadata.OutcomeTimedOut, metadata.OutcomeFailed, metadata.OutcomePrescreened:
		var maxBackoff time.Duration
		if g.MaxBackoffFunc != nil {
			maxBackoff = g.MaxBackoffFunc()
		}
		if _, err := g.st.RecordFetchFailure(ctx, hash, outcomeLabel(outcome), g.now(), maxBackoff); err != nil {
			g.logger.Printf("admission: record failure %s: %v", hash, err)
		}
	}
}

// outcomeLabel is the fetch_attempts.last_outcome spelling of a
// failed outcome.
func outcomeLabel(o metadata.FetchOutcome) string {
	switch o {
	case metadata.OutcomeTimedOut:
		return "timeout"
	case metadata.OutcomePrescreened:
		return "prescreen"
	default:
		return "failed"
	}
}

// Skipped returns the cumulative refusal counts since process
// start: hashes refused because they are already indexed, and
// hashes refused by the failure backoff. The caller mirrors deltas
// of these into the stats table, the same treatment the fetcher's
// own counters get.
func (g *Gate) Skipped() (stored, backoff int64) {
	return g.skippedStored.Load(), g.skippedBackoff.Load()
}
