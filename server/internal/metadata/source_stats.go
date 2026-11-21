package metadata

import "sync"

// Name returns the discovery source's stable lowercase label — the
// value published in the fetch section's by_source rows and the
// middle segment of its stats-table keys (see SourceStatKey).
// Out-of-range values name as the default source, dht, matching
// normalizeSource.
func (s Source) Name() string {
	switch s {
	case SourceScrape:
		return "scrape"
	case SourceFeed:
		return "feed"
	default:
		return "dht"
	}
}

// AllSources lists the discovery sources in canonical order — the
// order by_source rows are published in everywhere.
func AllSources() []Source {
	return []Source{SourceDHT, SourceScrape, SourceFeed}
}

// ParseSource maps a Name() label back to its Source. Unknown
// labels fold into SourceDHT, matching normalizeSource: a record
// whose source tag was lost in transit is a main-pipeline record,
// and its attribution must land somewhere truthful.
func ParseSource(name string) Source {
	switch name {
	case "scrape":
		return SourceScrape
	case "feed":
		return SourceFeed
	default:
		return SourceDHT
	}
}

// normalizeSource folds an out-of-range Source into SourceDHT. The
// tag rides on the queue Item from the discovery bridge to the
// fetch worker; a fetch whose tag was somehow lost is a
// main-pipeline fetch, and its outcome must still land somewhere
// truthful rather than corrupt the attribution table.
func normalizeSource(s Source) Source {
	switch s {
	case SourceDHT, SourceScrape, SourceFeed:
		return s
	default:
		return SourceDHT
	}
}

// FetchSourceStats is one discovery source's cumulative fetch
// counters: how many fetches its discoveries accounted for
// (Attempts) and how those fetches ended. Attempts counts fetches
// started; the four outcome counters count fetches that reached a
// terminal outcome, so Attempts exceeds their sum by the fetches
// in flight plus any aborted by shutdown — exactly the relationship
// the global counters have to a process's fetch history.
type FetchSourceStats struct {
	Source           Source
	Attempts         int64
	Fetched          int64
	TimedOut         int64
	Failed           int64
	PrescreenDropped int64
}

// The per-source counters' names inside their stats-table keys
// (SourceStatKey). They deliberately match the fetch section's
// field names in the API payload.
const (
	SourceCounterAttempts         = "attempts"
	SourceCounterFetched          = "fetched"
	SourceCounterTimedOut         = "timed_out"
	SourceCounterFailed           = "failed"
	SourceCounterPrescreenDropped = "prescreen_dropped"
)

// SourceStatKey is the stats-table key the cmd/server stats mirror
// persists one per-source fetch counter under, and the key the API
// reads it back from: fetch_<source>_<counter>, e.g.
// fetch_scrape_timed_out.
func SourceStatKey(src Source, counter string) string {
	return "fetch_" + normalizeSource(src).Name() + "_" + counter
}

// SourceTracker accumulates per-source fetch outcome counters. All
// three fetch foundations embed one and record through it, so the
// attribution means the same thing whichever foundation is active —
// the tag lives on the Item, above the Backend contract, and each
// foundation only has to hand it to Attempt/Outcome at the same
// points where it moves its global counters. The zero value is
// ready to use.
type SourceTracker struct {
	mu sync.Mutex
	by [3]struct {
		attempts, fetched, timedOut, failed, prescreenDropped int64
	}
}

// Attempt records that a fetch of a src-tagged item has started.
func (t *SourceTracker) Attempt(src Source) {
	t.mu.Lock()
	t.by[normalizeSource(src)].attempts++
	t.mu.Unlock()
}

// Outcome records a fetch's terminal outcome under its source. It
// takes the same FetchOutcome the foundations report to
// Config.ResultFunc, so a foundation's outcome site moves the
// global counter, the per-source counter and the report together.
func (t *SourceTracker) Outcome(src Source, outcome FetchOutcome) {
	t.mu.Lock()
	c := &t.by[normalizeSource(src)]
	switch outcome {
	case OutcomeFetched:
		c.fetched++
	case OutcomeTimedOut:
		c.timedOut++
	case OutcomeFailed:
		c.failed++
	case OutcomePrescreened:
		c.prescreenDropped++
	}
	t.mu.Unlock()
}

// Stats returns the per-source counters in canonical order.
func (t *SourceTracker) Stats() []FetchSourceStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]FetchSourceStats, 0, len(AllSources()))
	for _, src := range AllSources() {
		c := t.by[src]
		out = append(out, FetchSourceStats{
			Source:           src,
			Attempts:         c.attempts,
			Fetched:          c.fetched,
			TimedOut:         c.timedOut,
			Failed:           c.failed,
			PrescreenDropped: c.prescreenDropped,
		})
	}
	return out
}
