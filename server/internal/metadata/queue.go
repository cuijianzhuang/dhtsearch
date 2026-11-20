package metadata

import (
	"container/heap"
	"context"
	"net/netip"
	"sync"
	"time"
)

// Source identifies where a queued infohash came from. It is a
// priority signal (a curated feed hash is likelier to be alive and
// wanted than a hash sampled at random from the DHT) and the
// fetch-attribution tag: every fetch outcome is counted under its
// item's source (see SourceTracker), so the operator can see which
// discovery channel actually produces torrents.
type Source int

const (
	// SourceDHT is a plain crawler discovery with no scrape information.
	SourceDHT Source = iota
	// SourceScrape is a tracker-scraped discovery; the item carries the
	// best seeder count any tracker reported.
	SourceScrape
	// SourceFeed is a publisher-feed (curated) discovery.
	SourceFeed
)

// Item is one infohash waiting for a metadata fetch slot.
type Item struct {
	Hash string
	// Seeders is the tracker-reported seeder count, or -1 when the hash
	// was never scraped. Unknown counts rank as zero: a hash nobody
	// reported on is treated like a hash reported dead, not punished
	// below it — DHT-only swarms are exactly the ones trackers miss.
	Seeders int32
	Source  Source
	// At is when the hash reached this queue. Among otherwise equal
	// items the fresher one goes first: swarm liveness decays, and the
	// hashes a sample just reported are the ones most likely to still
	// have reachable peers.
	At time.Time
	// Peers carries DHT peer hints for this hash: addresses the
	// crawler's DHT side already knows are in the swarm (announce_peer
	// senders, get_peers reply values — see the crawler's peer-hint
	// cache), attached by the queue bridge as the item is built and
	// handed to the fetcher as a warm start alongside the magnet's
	// tracker-supplied peers. A hint is fetch input only: it plays no
	// part in queue priority, and a stale or dead hint costs nothing
	// but a failed dial inside the normal fetch budget. Nil for items
	// whose source carries no hints. Any fetcher backend (the
	// incumbent torrent-client one, or a native BEP 9 one) consumes
	// the same field: dial these addresses for this hash along with
	// whatever else it has.
	Peers []netip.AddrPort
}

// QueueStats is a snapshot of queue counters.
type QueueStats struct {
	Depth   int   // items currently waiting
	Pushed  int64 // items accepted
	Deduped int64 // pushes refused: hash already queued
	Dropped int64 // pushes refused: queue full and item not better than the worst queued
	Evicted int64 // queued items pushed out by a better incoming item
}

// DefaultQueueCap bounds the fetch queue. It matches the scraper's own
// queue cap, so the two stages together buffer at most ~32k hashes.
const DefaultQueueCap = 1 << 14

// Queue is a bounded, deduplicating priority queue feeding the metadata
// workers. The tracker scraper already ranks by seeder count when it is
// enabled; this queue is what gives the pipeline the same treatment when
// scrape is off (previously a plain FIFO channel), and it carries the
// scraper's seeder counts through when it is on, so the ranking survives
// the hand-off instead of being flattened back to arrival order.
//
// Priority, highest first: seeder count (unknown = 0), then source
// (feed > scrape > DHT), then freshness (newest first), then arrival
// order. A hash is queued at most once; the crawler's seen-ring remains
// the primary dedup, this is the second line for the window where a
// hash is re-reported while it still waits here.
type Queue struct {
	mu     sync.Mutex
	notify chan struct{} // 1-buffered wakeup for blocked Pops
	h      itemHeap
	seen   map[string]struct{}
	cap    int
	seq    uint64

	pushed  int64
	deduped int64
	dropped int64
	evicted int64
}

// NewQueue builds a queue holding at most cap items (<= 0 selects
// DefaultQueueCap).
func NewQueue(cap int) *Queue {
	if cap <= 0 {
		cap = DefaultQueueCap
	}
	return &Queue{
		notify: make(chan struct{}, 1),
		seen:   make(map[string]struct{}),
		cap:    cap,
	}
}

type qentry struct {
	item Item
	seq  uint64
}

// effSeeders folds "unknown" (-1) into 0.
func effSeeders(it Item) int32 {
	if it.Seeders < 0 {
		return 0
	}
	return it.Seeders
}

// outranks reports whether a should be fetched before b.
func outranks(a, b qentry) bool {
	if sa, sb := effSeeders(a.item), effSeeders(b.item); sa != sb {
		return sa > sb
	}
	if a.item.Source != b.item.Source {
		return a.item.Source > b.item.Source
	}
	if !a.item.At.Equal(b.item.At) {
		return a.item.At.After(b.item.At)
	}
	return a.seq < b.seq
}

type itemHeap []qentry

func (h itemHeap) Len() int           { return len(h) }
func (h itemHeap) Less(i, j int) bool { return outranks(h[i], h[j]) }
func (h itemHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *itemHeap) Push(x any)        { *h = append(*h, x.(qentry)) }
func (h *itemHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	*h = old[:n-1]
	return e
}

// Push offers an item to the queue and reports whether it was queued.
// When the queue is full, the incoming item replaces the worst queued
// item if it outranks it (the evicted one is counted, mirroring the
// scraper's overflow behaviour); otherwise the incoming item is dropped.
func (q *Queue) Push(it Item) bool {
	if it.Hash == "" {
		return false
	}
	if it.At.IsZero() {
		it.At = time.Now()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.seen[it.Hash]; ok {
		q.deduped++
		return false
	}
	e := qentry{item: it, seq: q.seq}
	q.seq++
	if len(q.h) >= q.cap {
		wi := 0
		for i := 1; i < len(q.h); i++ {
			if outranks(q.h[wi], q.h[i]) {
				wi = i
			}
		}
		if !outranks(e, q.h[wi]) {
			q.dropped++
			return false
		}
		delete(q.seen, q.h[wi].item.Hash)
		heap.Remove(&q.h, wi)
		q.evicted++
	}
	heap.Push(&q.h, e)
	q.seen[it.Hash] = struct{}{}
	q.pushed++
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return true
}

// Pop removes and returns the highest-priority item, blocking until one
// is available or ctx ends.
func (q *Queue) Pop(ctx context.Context) (Item, bool) {
	for {
		q.mu.Lock()
		if len(q.h) > 0 {
			e := heap.Pop(&q.h).(qentry)
			delete(q.seen, e.item.Hash)
			q.mu.Unlock()
			return e.item, true
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return Item{}, false
		case <-q.notify:
		}
	}
}

// Len reports how many items are waiting.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.h)
}

// Stats returns a snapshot of the queue counters.
func (q *Queue) Stats() QueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return QueueStats{
		Depth:   len(q.h),
		Pushed:  q.pushed,
		Deduped: q.deduped,
		Dropped: q.dropped,
		Evicted: q.evicted,
	}
}
