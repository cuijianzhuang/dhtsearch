// Package searchstats counts what visitors search for, in
// aggregate only, for the homepage's 热门搜索 row and the public
// /api/trending-terms endpoint.
//
// Privacy is structural, not a policy promise: the only thing that
// ever leaves this package is (normalized term, display casing,
// count, day). No client address, no user agent, no session, no
// per-visitor record exists anywhere in the pipeline — the store
// schema (internal/store/searchterms.go) has no column that could
// hold one.
//
// The request path pays one mutex-guarded map update: Record does
// no I/O, never blocks on the database and cannot fail a search.
// A background Flush every few seconds writes the accumulated
// deltas to the store; a failed flush keeps its batch for the next
// round, so a transient store error loses nothing.
package searchstats

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"dhtsearch/server/internal/store"
)

const (
	// flushInterval is how often Run persists the pending deltas.
	// Five seconds keeps the day-bucket attribution honest (a
	// search is stamped with the day it was recorded, not the day
	// of the next flush — see pendingKey) while batching bursts
	// of identical searches into one upsert each.
	flushInterval = 5 * time.Second
	// maxPendingTerms bounds the pending map by distinct
	// (day, term) pairs. Beyond it, NEW terms are dropped until
	// the next flush (terms already pending keep counting): a
	// flood of unique junk queries must not grow memory without
	// bound, and dropping tail terms costs the hot list nothing.
	maxPendingTerms = 4096
	// flushTimeout bounds one store write, matching the server's
	// unified store-write timeout convention.
	flushTimeout = 15 * time.Second
	// errLogInterval throttles flush-failure logging: a store
	// outage would otherwise print one line per flush tick.
	errLogInterval = time.Minute
)

// pendingKey is one pending bucket: a term on a specific day. The
// day is captured at Record time so a flush that lands after
// midnight still attributes the search to the day it happened.
type pendingKey struct {
	day  int64
	term string
}

type pendingTerm struct {
	display string
	count   int64
}

// Service aggregates search-term counts in memory and flushes them
// to the store. Construct with New, feed with Record (the API's
// search handler), and run Run on the server's lifecycle context.
type Service struct {
	st     *store.Store
	logger *log.Logger
	now    func() time.Time

	mu         sync.Mutex
	pending    map[pendingKey]*pendingTerm
	dropped    int64 // new terms dropped because pending was full
	lastErrLog time.Time
}

// New builds a Service writing to st.
func New(st *store.Store, logger *log.Logger) *Service {
	if logger == nil {
		logger = log.Default()
	}
	return &Service{
		st:      st,
		logger:  logger,
		now:     time.Now,
		pending: make(map[pendingKey]*pendingTerm),
	}
}

// Record merges one counted search into the pending set. term and
// display come from store.NormalizeSearchTerm. It never blocks and
// never touches the store.
func (s *Service) Record(term, display string) {
	key := pendingKey{day: store.SearchTermDay(s.now()), term: term}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.pending[key]; ok {
		p.display = display // freshest casing wins
		p.count++
		return
	}
	if len(s.pending) >= maxPendingTerms {
		s.dropped++
		return
	}
	s.pending[key] = &pendingTerm{display: display, count: 1}
}

// Dropped reports how many new terms were dropped because the
// pending map was full. Observability for tests and logs; a nonzero
// value means the distinct-term flood outran the flush cadence.
func (s *Service) Dropped() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Flush writes every pending delta to the store, grouped by day.
// On error the whole batch is restored to the pending set (merged
// with anything recorded meanwhile) and the error is returned, so
// a transient store failure loses no counts.
func (s *Service) Flush(ctx context.Context) error {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return nil
	}
	batch := s.pending
	s.pending = make(map[pendingKey]*pendingTerm)
	s.mu.Unlock()

	byDay := make(map[int64][]store.SearchTermDelta)
	for key, p := range batch {
		byDay[key.day] = append(byDay[key.day], store.SearchTermDelta{
			Term: key.term, Display: p.display, Count: p.count,
		})
	}
	days := make([]int64, 0, len(byDay))
	for day := range byDay {
		days = append(days, day)
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })

	wctx, cancel := context.WithTimeout(ctx, flushTimeout)
	defer cancel()
	for _, day := range days {
		deltas := byDay[day]
		sort.Slice(deltas, func(i, j int) bool { return deltas[i].Term < deltas[j].Term })
		if err := s.st.RecordSearchTerms(wctx, day, deltas); err != nil {
			s.restore(batch)
			return err
		}
	}
	return nil
}

// restore merges a failed flush's batch back into the pending set.
func (s *Service) restore(batch map[pendingKey]*pendingTerm) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, p := range batch {
		if cur, ok := s.pending[key]; ok {
			cur.count += p.count
			// The batch's casing is older than anything recorded
			// since; keep the pending (fresher) display.
			continue
		}
		s.pending[key] = p
	}
}

// Run flushes on the flush cadence until ctx ends, then makes one
// final flush attempt so a clean shutdown loses at most nothing.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
			if err := s.Flush(fctx); err != nil {
				s.logger.Printf("searchstats: final flush: %v", err)
			}
			cancel()
			return
		case <-t.C:
			if err := s.Flush(ctx); err != nil {
				s.logErr(err)
			}
		}
	}
}

// logErr prints a flush failure at most once per errLogInterval.
func (s *Service) logErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.lastErrLog) < errLogInterval {
		return
	}
	s.lastErrLog = time.Now()
	s.logger.Printf("searchstats: flush: %v", err)
}
