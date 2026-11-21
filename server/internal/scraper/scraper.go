// Package scraper ranks discovered infohashes by swarm size before metadata
// fetching. Discovery yields far more infohashes than the fetch pool can
// consume, so which ones get a fetch slot decides what the index grows into.
// A BEP 15 UDP scrape answers "how many seeders?" for up to ~70 infohashes in
// a single packet — three orders of magnitude cheaper than a metadata fetch —
// so every discovered hash is scraped against a few large open trackers and
// queued by seeder count. Popular swarms (which skew toward current video
// content) are fetched first; they are also the fetches most likely to
// succeed, since peers are plentiful.
//
// Zero-seeder hashes are not discarded: they queue at priority 0 and are
// fetched whenever nothing better is waiting, so DHT-only content still
// trickles in. When the queue overflows, the lowest-priority entries are the
// ones evicted — the same hashes that were previously dropped at random.
package scraper

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent/tracker/udp"
)

const (
	defaultQueueCap  = 1 << 14
	defaultBatchMax  = 70 // BEP 15 allows ~74 per packet; stay under
	defaultBatchWait = 2 * time.Second
	defaultTimeout   = 10 * time.Second

	// scrapeWorkers bounds concurrent in-flight batches. Each worker mostly
	// waits on UDP round trips, so they are cheap; the bound exists so dead
	// trackers (every batch riding out the full timeout) shed load by
	// dropping batches instead of queueing them without limit.
	scrapeWorkers  = 8
	batchQueueSize = 16
)

// SwarmSample is one tracker's BEP 15 answer for one infohash:
// Seeders (complete), Leechers (incomplete) and Completed
// (downloaded) as the tracker reported them.
type SwarmSample struct {
	Seeders   int32
	Leechers  int32
	Completed int32
}

// SwarmStat is one infohash's aggregated swarm snapshot, handed to
// Config.Sink: each field is the maximum across the trackers that
// answered the round. Trackers track disjoint swarms and answer
// zero for hashes they do not know, so the maximum is the only
// combination that never lets one tracker's ignorance erase
// another's knowledge.
type SwarmStat struct {
	Hash      string
	Seeders   int32
	Leechers  int32
	Completed int32
}

// Config controls the scraper.
type Config struct {
	// Trackers are udp:// announce URLs to scrape. Non-UDP entries are
	// skipped (HTTP scrape exists but no large open tracker needs it here).
	Trackers []string
	// QueueCap bounds the priority queue; overflow evicts the lowest
	// seeder counts first.
	QueueCap int
	// BatchMax is the most infohashes packed into one scrape request.
	BatchMax int
	// BatchWait flushes a partial batch that has waited this long.
	BatchWait time.Duration
	// Timeout is the budget for scraping one batch against one tracker.
	Timeout time.Duration
	// Sink, when non-nil, receives every scrape round's aggregated
	// swarm snapshots (see SwarmStat) for persistence. It is called
	// from a scrape worker after the round's queue hand-off; hashes
	// no tracker answered are absent (nothing was learned about
	// them), while a hash a tracker answered all-zero for is present
	// with zeros — a real observation, not a gap. Nil disables
	// persistence; the queue ranking is unaffected either way.
	Sink func([]SwarmStat)
	// Logger, nil for log.Default().
	Logger *log.Logger
}

// Stats is a snapshot of scraper activity for the stats endpoint.
type Stats struct {
	QueueLen   int   // hashes waiting for a fetch slot
	Scraped    int64 // hashes scraped so far
	Seeded     int64 // of those, hashes with at least one seeder
	ScrapeErrs int64 // failed tracker requests (per tracker, per batch)
	Dropped    int64 // hashes dropped because all scrape workers were busy
	Evicted    int64 // hashes evicted from a full queue
}

// Item is one scraped infohash on its way to the fetch pool. The
// seeder count travels with the hash so a downstream priority queue
// (metadata.Queue) can keep ranking by it after the hand-off instead
// of flattening back to arrival order.
type Item struct {
	Hash    string
	Seeders int32
}

// trackerConn owns the UDP client for one tracker, recreating it after a
// failure (the conn client closes itself on read errors).
type trackerConn struct {
	host string // "host:port"
	mu   sync.Mutex
	cc   *udp.ConnClient
}

// scrapeFunc performs one BEP 15 scrape against one tracker. It is
// the real network call in production and a stub in tests.
type scrapeFunc func(ctx context.Context, tc *trackerConn, ihs [][20]byte) ([]SwarmSample, error)

// buildTrackers parses the configured announce URLs into tracker
// connections, skipping (and logging) the unusable ones.
func buildTrackers(raw []string, logger *log.Logger) []*trackerConn {
	var out []*trackerConn
	for _, u := range raw {
		host, err := trackerHost(u)
		if err != nil {
			logger.Printf("scraper: skipping tracker %q: %v", u, err)
			continue
		}
		out = append(out, &trackerConn{host: host})
	}
	return out
}

// queryTrackers scrapes ihs against every tracker in parallel and
// merges the answers: best[i] is the per-field maximum across the
// trackers that answered for hash i, and covered[i] records whether
// any tracker answered for it at all. A short response (fewer
// entries than requested) only covers its prefix. The returned
// count is the number of failed tracker requests. This merge is the
// one aggregation both the ingest pipeline and the background
// re-scraper use, so their snapshots are built identically.
func queryTrackers(ctx context.Context, trackers []*trackerConn, scrapeFn scrapeFunc, timeout time.Duration, ihs [][20]byte) (best []SwarmSample, covered []bool, errs int64) {
	best = make([]SwarmSample, len(ihs))
	covered = make([]bool, len(ihs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	var errCount atomic.Int64
	for _, tc := range trackers {
		wg.Add(1)
		go func(tc *trackerConn) {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			res, err := scrapeFn(sctx, tc, ihs)
			if err != nil {
				errCount.Add(1)
				return
			}
			mu.Lock()
			for i, n := range res {
				if i >= len(best) {
					break
				}
				covered[i] = true
				if n.Seeders > best[i].Seeders {
					best[i].Seeders = n.Seeders
				}
				if n.Leechers > best[i].Leechers {
					best[i].Leechers = n.Leechers
				}
				if n.Completed > best[i].Completed {
					best[i].Completed = n.Completed
				}
			}
			mu.Unlock()
		}(tc)
	}
	wg.Wait()
	return best, covered, errCount.Load()
}

// coveredStats builds a round's sink payload from its merged
// answers: only covered hashes appear. A hash no tracker answered
// for is absent — nothing was learned about it, and persisting an
// invented zero would corrupt the zero-seeder streak evidence —
// while a covered all-zero hash is present with zeros, a real
// observation.
func coveredStats(kept []string, best []SwarmSample, covered []bool) []SwarmStat {
	stats := make([]SwarmStat, 0, len(kept))
	for i, h := range kept {
		if !covered[i] {
			continue
		}
		stats = append(stats, SwarmStat{
			Hash:      h,
			Seeders:   best[i].Seeders,
			Leechers:  best[i].Leechers,
			Completed: best[i].Completed,
		})
	}
	return stats
}

// Scraper consumes discovered infohashes, scrapes them and re-emits them in
// descending seeder order.
type Scraper struct {
	cfg      Config
	trackers []*trackerConn
	logger   *log.Logger

	out      chan Item
	wake     chan struct{}
	feedDone chan struct{} // closed once nothing can enqueue anymore

	mu      sync.Mutex
	pending []Item // sorted by seeders descending; FIFO among equals

	cancel context.CancelFunc
	wg     sync.WaitGroup

	scraped    atomic.Int64
	seeded     atomic.Int64
	scrapeErrs atomic.Int64
	dropped    atomic.Int64
	evicted    atomic.Int64

	// scrapeTracker is swapped in tests to avoid network I/O.
	scrapeTracker scrapeFunc
}

// New validates the tracker list and builds a scraper. At least one valid
// udp:// tracker is required.
func New(cfg Config) (*Scraper, error) {
	if cfg.QueueCap <= 0 {
		cfg.QueueCap = defaultQueueCap
	}
	if cfg.BatchMax <= 0 || cfg.BatchMax > defaultBatchMax {
		cfg.BatchMax = defaultBatchMax
	}
	if cfg.BatchWait <= 0 {
		cfg.BatchWait = defaultBatchWait
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	s := &Scraper{
		cfg:           cfg,
		logger:        logger,
		out:           make(chan Item),
		wake:          make(chan struct{}, 1),
		feedDone:      make(chan struct{}),
		scrapeTracker: realScrape,
	}
	s.trackers = buildTrackers(cfg.Trackers, logger)
	if len(s.trackers) == 0 {
		return nil, fmt.Errorf("no scrapeable udp trackers in %v", cfg.Trackers)
	}
	return s, nil
}

// trackerHost extracts "host:port" from a udp:// announce URL.
func trackerHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "udp" {
		return "", fmt.Errorf("scheme %q is not scrapeable over BEP 15", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("no host")
	}
	return u.Host, nil
}

// Run starts the pipeline consuming hex infohashes from in. The reordered
// hashes are delivered on Out, which closes when in closes or the scraper is
// closed.
func (s *Scraper) Run(ctx context.Context, in <-chan string) {
	ctx, s.cancel = context.WithCancel(ctx)
	batches := make(chan []string, batchQueueSize)
	var feedWG sync.WaitGroup
	feedWG.Add(1)
	go s.collect(ctx, in, batches, &feedWG)
	for i := 0; i < scrapeWorkers; i++ {
		feedWG.Add(1)
		go s.scrapeWorker(ctx, batches, &feedWG)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		feedWG.Wait()
		close(s.feedDone)
	}()
	s.wg.Add(1)
	go s.pushLoop(ctx)
	s.logger.Printf("scraper: ranking infohashes via %d trackers", len(s.trackers))
}

// Out returns the channel of prioritized infohashes, each carrying
// the best seeder count the scrape found for it.
func (s *Scraper) Out() <-chan Item { return s.out }

// Stats returns a snapshot of the counters.
func (s *Scraper) Stats() Stats {
	s.mu.Lock()
	qlen := len(s.pending)
	s.mu.Unlock()
	return Stats{
		QueueLen:   qlen,
		Scraped:    s.scraped.Load(),
		Seeded:     s.seeded.Load(),
		ScrapeErrs: s.scrapeErrs.Load(),
		Dropped:    s.dropped.Load(),
		Evicted:    s.evicted.Load(),
	}
}

// Close stops the pipeline and closes tracker connections.
func (s *Scraper) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	for _, tc := range s.trackers {
		tc.mu.Lock()
		if tc.cc != nil {
			tc.cc.Close()
			tc.cc = nil
		}
		tc.mu.Unlock()
	}
}

// rescrapeBatchWorkers bounds how many batches of one Rescraper
// round are in flight at once. Each batch already fans out to every
// tracker in parallel, so a handful of batches saturates the round;
// the bound exists so a large re-scrape selection cannot burst
// tracker traffic the way the ingest pipeline's eight workers do.
const rescrapeBatchWorkers = 3

// Rescraper runs standalone scrape rounds over arbitrary hash
// lists — the background re-scraper's engine (see
// internal/rescrape). It shares the ingest pipeline's trackers,
// per-field-max merge and coverage rules (queryTrackers /
// coveredStats) but none of its queue machinery: the caller picks
// the hashes, and the round's snapshots come back in the same
// SwarmStat shape the ingest Sink persists, so both write paths
// land identical rows. Hashes no tracker answered for are absent
// from the result — a fully failed round returns nothing at all,
// never invented zeros.
type Rescraper struct {
	trackers []*trackerConn
	batchMax int
	timeout  time.Duration
	logger   *log.Logger

	// scrapeTracker is swapped in tests to avoid network I/O.
	scrapeTracker scrapeFunc
}

// NewRescraper validates the tracker list and builds a Rescraper.
// At least one valid udp:// tracker is required.
func NewRescraper(cfg Config) (*Rescraper, error) {
	batchMax := cfg.BatchMax
	if batchMax <= 0 || batchMax > defaultBatchMax {
		batchMax = defaultBatchMax
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	r := &Rescraper{
		batchMax:      batchMax,
		timeout:       timeout,
		logger:        logger,
		scrapeTracker: realScrape,
	}
	r.trackers = buildTrackers(cfg.Trackers, logger)
	if len(r.trackers) == 0 {
		return nil, fmt.Errorf("no scrapeable udp trackers in %v", cfg.Trackers)
	}
	return r, nil
}

// Scrape runs one re-scrape round over hashes and returns the
// round's aggregated snapshots (see the type comment for the
// coverage contract). Invalid hashes are skipped. Batches run a
// few at a time; a cancelled context returns whatever finished.
func (r *Rescraper) Scrape(ctx context.Context, hashes []string) []SwarmStat {
	type batch struct {
		ihs  [][20]byte
		kept []string
	}
	var batches []batch
	var cur batch
	flush := func() {
		if len(cur.ihs) > 0 {
			batches = append(batches, cur)
			cur = batch{}
		}
	}
	for _, h := range hashes {
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != 20 {
			continue
		}
		var ih [20]byte
		copy(ih[:], b)
		cur.ihs = append(cur.ihs, ih)
		cur.kept = append(cur.kept, h)
		if len(cur.ihs) >= r.batchMax {
			flush()
		}
	}
	flush()
	if len(batches) == 0 {
		return nil
	}

	results := make([][]SwarmStat, len(batches))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < rescrapeBatchWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				best, covered, _ := queryTrackers(ctx, r.trackers, r.scrapeTracker, r.timeout, batches[i].ihs)
				results[i] = coveredStats(batches[i].kept, best, covered)
			}
		}()
	}
send:
	for i := range batches {
		select {
		case <-ctx.Done():
			break send
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()

	var out []SwarmStat
	for _, res := range results {
		out = append(out, res...)
	}
	return out
}

// Close drops the tracker connections.
func (r *Rescraper) Close() {
	for _, tc := range r.trackers {
		tc.mu.Lock()
		if tc.cc != nil {
			tc.cc.Close()
			tc.cc = nil
		}
		tc.mu.Unlock()
	}
}

// collect batches incoming hashes for scraping. It always keeps draining in:
// if the scrape workers are saturated, a batch is dropped rather than letting
// backpressure reach the crawler's non-blocking push.
func (s *Scraper) collect(ctx context.Context, in <-chan string, batches chan<- []string, wg *sync.WaitGroup) {
	defer wg.Done()
	defer close(batches)
	var batch []string
	timer := time.NewTimer(s.cfg.BatchWait)
	if !timer.Stop() {
		<-timer.C
	}
	flush := func() {
		if len(batch) == 0 {
			return
		}
		select {
		case batches <- batch:
		default:
			s.dropped.Add(int64(len(batch)))
		}
		batch = nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case h, ok := <-in:
			if !ok {
				flush()
				return
			}
			if len(batch) == 0 {
				timer.Reset(s.cfg.BatchWait)
			}
			batch = append(batch, h)
			if len(batch) >= s.cfg.BatchMax {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				flush()
			}
		case <-timer.C:
			flush()
		}
	}
}

func (s *Scraper) scrapeWorker(ctx context.Context, batches <-chan []string, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case batch, ok := <-batches:
			if !ok {
				return
			}
			s.scrapeBatch(ctx, batch)
		}
	}
}

// scrapeBatch queries every tracker in parallel and enqueues each hash with
// the highest seeder count any tracker reported. A hash unknown everywhere
// (or a batch where every tracker errored) enqueues at zero — deprioritized,
// not lost. The round's full swarm answers (seeders, leechers, completed)
// are aggregated per-field-max and handed to the sink for persistence.
func (s *Scraper) scrapeBatch(ctx context.Context, hexes []string) {
	ihs := make([][20]byte, 0, len(hexes))
	kept := make([]string, 0, len(hexes))
	for _, h := range hexes {
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != 20 {
			continue
		}
		var ih [20]byte
		copy(ih[:], b)
		ihs = append(ihs, ih)
		kept = append(kept, h)
	}
	if len(ihs) == 0 {
		return
	}
	// covered[i] records whether any tracker actually answered for
	// hash i; the sink must not persist invented zeros for the rest.
	best, covered, errs := queryTrackers(ctx, s.trackers, s.scrapeTracker, s.cfg.Timeout, ihs)
	s.scrapeErrs.Add(errs)

	items := make([]Item, len(kept))
	var seededN int64
	for i, h := range kept {
		if best[i].Seeders > 0 {
			seededN++
		}
		items[i] = Item{Hash: h, Seeders: best[i].Seeders}
	}
	s.scraped.Add(int64(len(kept)))
	s.seeded.Add(seededN)
	s.enqueue(items)

	if s.cfg.Sink != nil {
		if stats := coveredStats(kept, best, covered); len(stats) > 0 {
			s.cfg.Sink(stats)
		}
	}
}

// enqueue merges a scraped batch into the priority queue. Sorting the whole
// slice per batch is deliberate: batches arrive every second or two and the
// queue is a few thousand entries, so a stable sort is both fast enough and
// gives FIFO order among equal seeder counts for free.
func (s *Scraper) enqueue(items []Item) {
	s.mu.Lock()
	s.pending = append(s.pending, items...)
	sort.SliceStable(s.pending, func(i, j int) bool {
		return s.pending[i].Seeders > s.pending[j].Seeders
	})
	if n := len(s.pending); n > s.cfg.QueueCap {
		s.evicted.Add(int64(n - s.cfg.QueueCap))
		s.pending = append([]Item(nil), s.pending[:s.cfg.QueueCap]...)
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// pushLoop feeds the fetcher the current best hash, blocking on the
// unbuffered out channel so priority is applied as late as possible. It
// closes out once the queue is empty and nothing upstream can refill it,
// mirroring how the crawler's channel closes on shutdown.
func (s *Scraper) pushLoop(ctx context.Context) {
	defer s.wg.Done()
	defer close(s.out)
	for {
		s.mu.Lock()
		var next Item
		ok := len(s.pending) > 0
		if ok {
			next = s.pending[0]
			s.pending = s.pending[1:]
		}
		s.mu.Unlock()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			case <-s.feedDone:
			}
			// Every enqueue happens before feedDone closes, so an empty
			// queue seen after this point is final.
			s.mu.Lock()
			empty := len(s.pending) == 0
			s.mu.Unlock()
			if empty {
				return
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case s.out <- next:
		}
	}
}

// realScrape performs one BEP 15 scrape. BEP 15 defines the response as one
// result per requested infohash, in request order, so alignment is by index.
func realScrape(ctx context.Context, tc *trackerConn, ihs [][20]byte) ([]SwarmSample, error) {
	cc, err := tc.conn()
	if err != nil {
		return nil, err
	}
	resp, err := cc.Client.Scrape(ctx, ihs)
	if err != nil {
		tc.reset(cc)
		return nil, err
	}
	if len(resp) > len(ihs) {
		resp = resp[:len(ihs)]
	}
	out := make([]SwarmSample, len(resp))
	for i, r := range resp {
		out[i] = SwarmSample{Seeders: r.Seeders, Leechers: r.Leechers, Completed: r.Completed}
	}
	return out, nil
}

func (tc *trackerConn) conn() (*udp.ConnClient, error) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.cc != nil {
		return tc.cc, nil
	}
	cc, err := udp.NewConnClient(udp.NewConnClientOpts{Network: "udp", Host: tc.host})
	if err != nil {
		return nil, err
	}
	tc.cc = cc
	return cc, nil
}

// reset drops the connection that just failed so the next scrape redials,
// unless another goroutine already replaced it.
func (tc *trackerConn) reset(old *udp.ConnClient) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.cc == old {
		old.Close()
		tc.cc = nil
	}
}
