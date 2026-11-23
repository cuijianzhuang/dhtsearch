package scraper

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Tracker quality scoring. A scrape round used to ask every
// configured tracker about every batch and wait for all of them:
// one dead tracker rode out the full per-batch timeout on every
// batch, and a tracker that answered but never knew a seeder cost
// the same round trip as the one carrying the swarm. TrackerQuality
// keeps a per-tracker score, fed by the scrape rounds themselves
// (both the ingest pipeline's and the re-scraper's — they share one
// instance, see Config.Quality), and queryTrackers uses it to:
//
//   - order each round's trackers so proven, productive trackers
//     are asked first,
//   - sit a tracker out of rounds once the evidence is conclusive
//     (enough queries, a blended score in the basement, and a
//     response ratio no healthy tracker has), so dead trackers stop
//     taxing every batch with a full timeout,
//   - keep re-probing a couple of sat-out trackers every round, so
//     a tracker that comes back to life is noticed and reinstated
//     instead of being banned by a stale verdict forever.
//
// The score mirrors the DHT node-quality model (see
// internal/crawler's nodeQuality): a success ratio over
// exponentially decayed observation weights (half-life
// trackerHalfLife) seeded with a neutral prior, blended with the
// tracker's seeded yield — of the hashes it answered for, the
// share it reported seeders for. Decay alone never changes a
// score; it shrinks the weight of old evidence so new observations
// move the score faster.
//
// Two protections keep the grading from eating the evidence the
// swarm pipeline runs on. A tracker that answers reliably is never
// sat out, however barren its answers: an all-zero answer is a
// real observation (it is what proves torrents dead), and a
// reliable tracker answers fast, so it costs the round nothing.
// And selection never returns an empty set — if every tracker is
// sat out, the round asks them all, exactly as before grading
// existed; a round that asked nobody could never learn anything.
const (
	// trackerHalfLife is how long an observation keeps half its
	// weight — the same magnitude as the DHT node quality's, and
	// for the same reason: scrape rounds run every few seconds, so
	// half an hour of evidence reacts within the hour to a tracker
	// going dark without flapping on a single dropped packet.
	trackerHalfLife = 30 * time.Minute
	// trackerPriorW is the prior weight given to each side
	// (success and failure) of a tracker with no observations.
	trackerPriorW = 1.0
	// minTrackerAttempts is the raw query count (scrape batches,
	// across both pipelines) a tracker needs before it can be sat
	// out: below it, a bad score is a small sample, not a verdict.
	minTrackerAttempts = 20
	// skipScore is the blended-score floor for sitting a tracker
	// out. A tracker that never answers decays toward a score of
	// ~0.01; one that answers a third of the time and never
	// reports a seeder sits at ~0.15 and sinks as failures pile
	// up.
	skipScore = 0.15
	// skipReliabilityCeiling: a tracker answering at least this
	// share of its queries is never sat out (see the package
	// comment — its zero answers are evidence, and it answers
	// fast).
	skipReliabilityCeiling = 0.8
	// explorePerRound is how many sat-out trackers each round
	// re-probes anyway, least-recently-queried first, so the
	// sat-out pool rotates through the rounds and every member is
	// re-tested regularly.
	explorePerRound = 2
	// maxTrackerEntries bounds the tracker. Entries are keyed by
	// the configured trackers' hosts, a list an operator types, so
	// the bound is a belt-and-braces cap, not a working limit.
	maxTrackerEntries = 4096
	// minProvenAttempts is the raw observation count a tracker
	// needs before Snapshot offers it for persistence: a tracker
	// queried twice carries no verdict worth restoring.
	minProvenAttempts = 3
)

// PersistedTracker is one tracker's quality state in durable form,
// the shape exchanged with the persistence hooks wired in
// cmd/server. The weights are the decayed observation weights
// (prior included), so a restored tracker resumes with its old
// score — discounted by however long the process was down, because
// decay is applied from UpdatedAt.
type PersistedTracker struct {
	Host      string  // "host:port" of the tracker, as in trackerConn
	SuccW     float64 // decayed response weight
	FailW     float64 // decayed failure weight
	SeededW   float64 // decayed weight of answered hashes it reported seeders for
	AskedW    float64 // decayed weight of hashes it answered for
	LatencyMs float64 // EWMA of successful scrape latency; 0 = none measured
	Attempts  int64   // raw lifetime queries at save time
	UpdatedAt int64   // unix seconds of the last observation
}

type trackerEntry struct {
	succW, failW float64
	seededW      float64
	askedW       float64
	latencyMs    float64 // EWMA over successful queries; 0 = none yet
	attempts     int64
	lastSeen     time.Time // last observation; drives UpdatedAt and exploration order
	lastDecay    time.Time // anchor for the next decay step
}

// reliability is the decayed response ratio. The weights never sum
// to zero (the prior floor in decayLocked), so this never divides
// by zero.
func (e *trackerEntry) reliability() float64 {
	return e.succW / (e.succW + e.failW)
}

// score blends reliability with seeded yield: a tracker that
// answers everything but never reports a seeder scores half of one
// that also finds the swarms. A tracker with no successful answer
// yet has no yield evidence and scores its bare reliability —
// which for an unknown tracker is the neutral prior, 0.5.
func (e *trackerEntry) score() float64 {
	r := e.reliability()
	if e.askedW <= 0 {
		return r
	}
	seededRate := e.seededW / e.askedW
	if seededRate > 1 {
		seededRate = 1
	}
	return r * (0.5 + 0.5*seededRate)
}

// skippable reports whether the evidence justifies sitting the
// tracker out of rounds: enough queries to be a verdict, a blended
// score in the basement, and a response ratio below the ceiling
// that protects reliable answerers.
func (e *trackerEntry) skippable() bool {
	return e.attempts >= minTrackerAttempts &&
		e.reliability() < skipReliabilityCeiling &&
		e.score() < skipScore
}

// TrackerQuality is the per-tracker score tracker. All methods are
// safe for concurrent use; the ingest scraper's workers, the
// re-scraper's rounds and the persistence loop all feed or read it.
// The zero value is not usable; build one with NewTrackerQuality.
type TrackerQuality struct {
	mu      sync.Mutex
	entries map[string]*trackerEntry
	now     func() time.Time // tests substitute a fake clock
}

// NewTrackerQuality returns an empty tracker: every tracker scores
// the neutral prior until rounds teach it otherwise.
func NewTrackerQuality() *TrackerQuality {
	return &TrackerQuality{
		entries: make(map[string]*trackerEntry),
		now:     time.Now,
	}
}

// decayLocked discounts e's observation weights by the time since
// the last decay step (e.lastDecay, which it advances — reads like
// score therefore decay idempotently instead of compounding). The
// ratios (and therefore the scores) are unchanged by decay alone —
// what shrinks is the weight of the old evidence. A floor keeps
// the sums from underflowing to zero over very long idles: the
// weights are rescaled to the prior sums with ratios preserved.
func (q *TrackerQuality) decayLocked(e *trackerEntry, now time.Time) {
	dt := now.Sub(e.lastDecay)
	if dt <= 0 {
		return
	}
	e.lastDecay = now
	f := math.Pow(0.5, float64(dt)/float64(trackerHalfLife))
	e.succW *= f
	e.failW *= f
	e.seededW *= f
	e.askedW *= f
	if sum := e.succW + e.failW; sum < 2*trackerPriorW {
		if sum <= 0 {
			e.succW, e.failW = trackerPriorW, trackerPriorW
		} else {
			scale := 2 * trackerPriorW / sum
			e.succW *= scale
			e.failW *= scale
		}
	}
	if e.askedW > 0 && e.askedW < trackerPriorW {
		scale := trackerPriorW / e.askedW
		e.seededW *= scale
		e.askedW *= scale
	}
}

// entryLocked returns the entry for host, creating it with prior
// weights when create is set. It returns nil when the tracker is
// at its bound and the host is new.
func (q *TrackerQuality) entryLocked(host string, create bool) *trackerEntry {
	if e, ok := q.entries[host]; ok {
		return e
	}
	if !create || len(q.entries) >= maxTrackerEntries {
		return nil
	}
	e := &trackerEntry{succW: trackerPriorW, failW: trackerPriorW}
	q.entries[host] = e
	return e
}

// record folds one scrape query's outcome into host's score.
// covered is how many of the batch's hashes the tracker answered
// for and seeded how many of those it reported seeders for; both
// are zero on failure, as is latency (a timeout's duration says
// nothing about the tracker's speed when it does answer).
func (q *TrackerQuality) record(host string, ok bool, latency time.Duration, covered, seeded int) {
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	e := q.entryLocked(host, true)
	if e == nil {
		return
	}
	if e.lastDecay.IsZero() {
		e.lastDecay = now
	}
	q.decayLocked(e, now)
	e.attempts++
	e.lastSeen = now
	if !ok {
		e.failW++
		return
	}
	e.succW++
	e.askedW += float64(covered)
	e.seededW += float64(seeded)
	if latency > 0 {
		ms := float64(latency) / float64(time.Millisecond)
		if e.latencyMs == 0 {
			e.latencyMs = ms
		} else {
			e.latencyMs = 0.75*e.latencyMs + 0.25*ms
		}
	}
}

// Score returns host's current blended score in [0, 1]; unknown
// trackers get the neutral prior 0.5.
func (q *TrackerQuality) Score(host string) float64 {
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	e := q.entryLocked(host, false)
	if e == nil {
		return 0.5
	}
	q.decayLocked(e, now)
	return e.score()
}

// selectTrackers picks the trackers one scrape round asks, best
// first: proven trackers by descending score, then up to
// explorePerRound sat-out trackers (least recently queried first)
// re-probed against their verdict. Unknown trackers score the
// neutral prior and are never sat out. The result is never empty
// when trackers is not: a round that would ask nobody asks
// everybody, so a network-wide outage cannot freeze the grading
// at its last verdict.
func (q *TrackerQuality) selectTrackers(trackers []*trackerConn) []*trackerConn {
	if len(trackers) <= 1 {
		return trackers
	}
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	type cand struct {
		tc       *trackerConn
		score    float64
		skip     bool
		lastSeen time.Time
	}
	cands := make([]cand, 0, len(trackers))
	var skipped []cand
	for _, tc := range trackers {
		c := cand{tc: tc, score: 0.5}
		if e := q.entryLocked(tc.host, false); e != nil {
			q.decayLocked(e, now)
			c.score = e.score()
			c.skip = e.skippable()
			c.lastSeen = e.lastSeen
		}
		if c.skip {
			skipped = append(skipped, c)
		} else {
			cands = append(cands, c)
		}
	}
	// Explore the sat-out pool oldest-verdict first; whoever is
	// re-probed records a fresh observation, so the pool rotates.
	sort.SliceStable(skipped, func(i, j int) bool {
		return skipped[i].lastSeen.Before(skipped[j].lastSeen)
	})
	for i := 0; i < len(skipped) && i < explorePerRound; i++ {
		cands = append(cands, skipped[i])
	}
	if len(cands) == 0 {
		return trackers
	}
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].score > cands[j].score
	})
	out := make([]*trackerConn, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.tc)
	}
	return out
}

// Seed loads persisted trackers into the tracker. Weights are
// taken as saved; lastSeen is the saved observation time, so the
// first decay after startup discounts the score by the downtime
// automatically.
func (q *TrackerQuality) Seed(trackers []PersistedTracker) {
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, p := range trackers {
		if p.Host == "" {
			continue
		}
		e := q.entryLocked(p.Host, true)
		if e == nil {
			continue
		}
		e.succW, e.failW = p.SuccW, p.FailW
		if e.succW+e.failW <= 0 {
			e.succW, e.failW = trackerPriorW, trackerPriorW
		}
		e.seededW, e.askedW = p.SeededW, p.AskedW
		e.latencyMs = p.LatencyMs
		e.attempts = p.Attempts
		e.lastSeen = time.Unix(p.UpdatedAt, 0)
		if e.lastSeen.After(now) {
			e.lastSeen = now
		}
		e.lastDecay = e.lastSeen
	}
}

// Snapshot returns the proven trackers' state for persistence,
// best score first. Trackers with fewer than minProvenAttempts
// observations are left out: their verdicts are not worth
// restoring, and leaving them out keeps the snapshot (and the
// table behind it) self-pruning, since saves replace it whole.
func (q *TrackerQuality) Snapshot() []PersistedTracker {
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]PersistedTracker, 0, len(q.entries))
	for host, e := range q.entries {
		q.decayLocked(e, now)
		if e.attempts < minProvenAttempts {
			continue
		}
		out = append(out, PersistedTracker{
			Host:      host,
			SuccW:     e.succW,
			FailW:     e.failW,
			SeededW:   e.seededW,
			AskedW:    e.askedW,
			LatencyMs: e.latencyMs,
			Attempts:  e.attempts,
			UpdatedAt: e.lastSeen.Unix(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		si := out[i].SuccW / (out[i].SuccW + out[i].FailW)
		sj := out[j].SuccW / (out[j].SuccW + out[j].FailW)
		if si != sj {
			return si > sj
		}
		return out[i].Attempts > out[j].Attempts
	})
	return out
}
