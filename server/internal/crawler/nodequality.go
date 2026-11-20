package crawler

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Node quality scoring. The crawler asks thousands of DHT nodes for
// samples, but nodes are not equal: some answer every time in tens of
// milliseconds, some answer rarely, and a long tail never answers at
// all — yet the sampler queue used to treat them identically, and the
// routing table forgot everything at restart. nodeQuality keeps a
// per-node score in memory, fed by the two query/response exchanges
// the crawler itself performs (sample_infohashes and find_node), and
// the crawler uses it to:
//
//   - order the routing-table refill so high-score nodes are offered
//     to the samplers first,
//   - shorten the resample interval of high-score nodes and lengthen
//     that of low-score ones,
//   - stop offering nodes that fail persistently (a cooldown ban),
//   - persist the best nodes across restarts (see PersistedNode), so
//     bootstrap starts from proven nodes instead of only the five
//     public routers.
//
// The score is a success ratio over exponentially decayed observation
// weights (half-life qualityHalfLife) seeded with a neutral prior, so
// an unknown node scores 0.5, recent behaviour dominates old
// behaviour, and no node's ancient glory keeps it on top forever: its
// weights decay toward the prior floor while fresh nodes accumulate
// real observations.
const (
	// qualityHalfLife is how long an observation keeps half its
	// weight. Thirty minutes is short next to a node's multi-day
	// uptime but long next to the 5s refill cycle, so scores react
	// within an hour to a node going dark without flapping on a
	// single lost UDP packet.
	qualityHalfLife = 30 * time.Minute
	// qualityPriorW is the prior weight given to each side (success
	// and failure) of a node with no observations.
	qualityPriorW = 1.0
	// banThreshold consecutive failures park a node for banCooldown.
	// One failure is a dropped packet; five in a row, with the
	// resample gate spacing the attempts out, is a dead or hostile
	// node not worth the samplers' slots.
	banThreshold = 5
	banCooldown  = 30 * time.Minute
	// maxQualityEntries bounds the tracker. At the cap, entries not
	// seen for qualityPruneAge are dropped; if nothing is stale the
	// new node is simply not tracked (re-learning it costs one query).
	maxQualityEntries = 1 << 15
	qualityPruneAge   = time.Hour
	// minProvenAttempts is the raw observation count a node needs
	// before Top offers it for persistence: a node that answered
	// twice in a row at first sight may still be a fluke.
	minProvenAttempts = 3
)

// PersistedNode is one node's quality state in durable form, the
// shape exchanged with the persistence hooks in Config. The weights
// are the decayed observation weights (prior included), so a restored
// node resumes with its old score — discounted by however long the
// process was down, because decay is applied from UpdatedAt.
type PersistedNode struct {
	Addr      string  // host:port, the krpc.NodeAddr string form
	SuccW     float64 // decayed success weight
	FailW     float64 // decayed failure weight
	LatencyMs float64 // EWMA of successful query latency; 0 = none measured
	Attempts  int64   // raw lifetime observations at save time
	UpdatedAt int64   // unix seconds of the last observation
}

// QualitySummary aggregates the tracker for Stats.
type QualitySummary struct {
	Tracked  int     // nodes with any tracker entry
	AvgScore float64 // mean score over nodes with >= 1 observation; 0 when none
	Banned   int     // nodes currently inside a failure cooldown
}

type nodeEntry struct {
	succW, failW float64
	latencyMs    float64 // EWMA over successful queries; 0 = none yet
	attempts     int64
	consecFails  int
	bannedUntil  time.Time
	lastSeen     time.Time // last observation or Touch; drives pruning and UpdatedAt
	lastDecay    time.Time // anchor for the next decay step
}

// score is the decayed success ratio. The weights never sum to zero
// (the prior floor in decayLocked), so this never divides by zero.
func (e *nodeEntry) score() float64 {
	return e.succW / (e.succW + e.failW)
}

// nodeQuality is the per-node score tracker. All methods are safe for
// concurrent use; the samplers, the walk loop and the persistence
// loop all feed or read it.
type nodeQuality struct {
	mu      sync.Mutex
	entries map[string]*nodeEntry
	now     func() time.Time // tests substitute a fake clock
}

func newNodeQuality() *nodeQuality {
	return &nodeQuality{
		entries: make(map[string]*nodeEntry),
		now:     time.Now,
	}
}

// decayLocked discounts e's observation weights by the time since
// the last decay step (e.lastDecay, which it advances — reads like
// Score therefore decay idempotently instead of compounding). The
// ratio (and therefore the score) is unchanged by decay alone — what
// shrinks is the weight of the old evidence, so new observations move
// the score faster. A floor keeps the sum from underflowing to zero
// over very long idles: the weights are rescaled to the prior sum
// with the ratio preserved.
func (q *nodeQuality) decayLocked(e *nodeEntry, now time.Time) {
	dt := now.Sub(e.lastDecay)
	if dt <= 0 {
		return
	}
	e.lastDecay = now
	f := math.Pow(0.5, float64(dt)/float64(qualityHalfLife))
	e.succW *= f
	e.failW *= f
	if sum := e.succW + e.failW; sum < 2*qualityPriorW {
		if sum <= 0 {
			e.succW, e.failW = qualityPriorW, qualityPriorW
		} else {
			scale := 2 * qualityPriorW / sum
			e.succW *= scale
			e.failW *= scale
		}
	}
}

// entryLocked returns the entry for addr, creating it with prior
// weights when create is set. It returns nil when the tracker is full
// of fresher entries and the node cannot be admitted.
func (q *nodeQuality) entryLocked(addr string, now time.Time, create bool) *nodeEntry {
	if e, ok := q.entries[addr]; ok {
		return e
	}
	if !create {
		return nil
	}
	if len(q.entries) >= maxQualityEntries {
		for k, e := range q.entries {
			if now.Sub(e.lastSeen) > qualityPruneAge {
				delete(q.entries, k)
			}
		}
		if len(q.entries) >= maxQualityEntries {
			return nil
		}
	}
	e := &nodeEntry{succW: qualityPriorW, failW: qualityPriorW, lastSeen: now, lastDecay: now}
	q.entries[addr] = e
	return e
}

// Touch registers addr as a known node without recording an
// observation, so the tracked pool reflects every node the crawler
// has met, not only the ones it has already queried.
func (q *nodeQuality) Touch(addr string) {
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	if e := q.entryLocked(addr, now, true); e != nil {
		e.lastSeen = now
	}
}

// Record folds one query outcome into addr's score. latency is the
// round-trip of a successful query; pass 0 on failure (a timeout's
// duration says nothing about the node's speed when it does answer).
func (q *nodeQuality) Record(addr string, ok bool, latency time.Duration) {
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	e := q.entryLocked(addr, now, true)
	if e == nil {
		return
	}
	q.decayLocked(e, now)
	e.attempts++
	e.lastSeen = now
	if ok {
		e.succW++
		e.consecFails = 0
		e.bannedUntil = time.Time{}
		if latency > 0 {
			ms := float64(latency) / float64(time.Millisecond)
			if e.latencyMs == 0 {
				e.latencyMs = ms
			} else {
				e.latencyMs = 0.75*e.latencyMs + 0.25*ms
			}
		}
		return
	}
	e.failW++
	e.consecFails++
	if e.consecFails >= banThreshold {
		e.bannedUntil = now.Add(banCooldown)
	}
}

// Score returns addr's current score in [0, 1]; unknown nodes get the
// neutral prior 0.5.
func (q *nodeQuality) Score(addr string) float64 {
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	e := q.entryLocked(addr, now, false)
	if e == nil {
		return 0.5
	}
	q.decayLocked(e, now)
	return e.score()
}

// Banned reports whether addr is parked in a failure cooldown. An
// expired ban clears itself (and the failure streak) on first check,
// so a node that was dead an hour ago gets fresh chances instead of
// staying banned by a stale verdict it can never appeal — it is never
// queried while banned.
func (q *nodeQuality) Banned(addr string) bool {
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	e := q.entryLocked(addr, now, false)
	if e == nil || e.bannedUntil.IsZero() {
		return false
	}
	if now.Before(e.bannedUntil) {
		return true
	}
	e.bannedUntil = time.Time{}
	e.consecFails = 0
	return false
}

// Seed loads persisted nodes into the tracker and returns the
// addresses admitted, best first. Weights are taken as saved;
// lastSeen is the saved observation time, so the first decay after
// startup discounts the score by the downtime automatically.
func (q *nodeQuality) Seed(nodes []PersistedNode) []string {
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	var addrs []string
	for _, n := range nodes {
		if n.Addr == "" {
			continue
		}
		e := q.entryLocked(n.Addr, now, true)
		if e == nil {
			continue
		}
		e.succW, e.failW = n.SuccW, n.FailW
		if e.succW+e.failW <= 0 {
			e.succW, e.failW = qualityPriorW, qualityPriorW
		}
		e.latencyMs = n.LatencyMs
		e.attempts = n.Attempts
		e.lastSeen = time.Unix(n.UpdatedAt, 0)
		e.lastDecay = e.lastSeen
		addrs = append(addrs, n.Addr)
	}
	return addrs
}

// Top returns the n best proven nodes (at least minProvenAttempts
// observations, score at least minScore, not currently banned),
// best first: score, then evidence weight, then latency. It is the
// snapshot the persistence loop saves.
func (q *nodeQuality) Top(n int, minScore float64) []PersistedNode {
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	type cand struct {
		p      PersistedNode
		weight float64
	}
	var cands []cand
	for addr, e := range q.entries {
		q.decayLocked(e, now)
		if e.attempts < minProvenAttempts || e.score() < minScore {
			continue
		}
		if !e.bannedUntil.IsZero() && now.Before(e.bannedUntil) {
			continue
		}
		cands = append(cands, cand{
			p: PersistedNode{
				Addr:      addr,
				SuccW:     e.succW,
				FailW:     e.failW,
				LatencyMs: e.latencyMs,
				Attempts:  e.attempts,
				UpdatedAt: e.lastSeen.Unix(),
			},
			weight: e.succW + e.failW,
		})
	}
	sort.Slice(cands, func(i, j int) bool {
		si, sj := cands[i].p.SuccW/(cands[i].p.SuccW+cands[i].p.FailW),
			cands[j].p.SuccW/(cands[j].p.SuccW+cands[j].p.FailW)
		if si != sj {
			return si > sj
		}
		if cands[i].weight != cands[j].weight {
			return cands[i].weight > cands[j].weight
		}
		// Lower latency first; 0 (unmeasured) sorts last.
		li, lj := cands[i].p.LatencyMs, cands[j].p.LatencyMs
		if li == 0 {
			li = math.Inf(1)
		}
		if lj == 0 {
			lj = math.Inf(1)
		}
		return li < lj
	})
	if len(cands) > n {
		cands = cands[:n]
	}
	out := make([]PersistedNode, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.p)
	}
	return out
}

// Summary aggregates the tracker for Stats.
func (q *nodeQuality) Summary() QualitySummary {
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	sum := QualitySummary{Tracked: len(q.entries)}
	var total float64
	var scored int
	for _, e := range q.entries {
		if !e.bannedUntil.IsZero() && now.Before(e.bannedUntil) {
			sum.Banned++
		}
		if e.attempts == 0 {
			continue
		}
		q.decayLocked(e, now)
		total += e.score()
		scored++
	}
	if scored > 0 {
		sum.AvgScore = total / float64(scored)
	}
	return sum
}
