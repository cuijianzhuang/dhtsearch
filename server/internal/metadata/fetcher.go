// Package metadata fetches torrent metainfo (BEP 9) for discovered
// infohashes with a bounded worker pool, without downloading payload data.
package metadata

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"

	"dhtsearch/server/internal/filter"
)

// Record is the extracted metadata of one torrent.
type Record struct {
	InfoHash  string
	Name      string
	TotalSize int64
	FileCount int
	Files     []filter.File
}

// Request is one infohash to fetch, carrying the swarm size the scraper
// measured for it. Seeders is negative when nothing measured it — scraping
// disabled, or hashes coming straight off the crawler.
type Request struct {
	InfoHash string
	Seeders  int32
}

// SeedersUnknown marks a request that never passed through a tracker scrape.
const SeedersUnknown = -1

// Timeout multipliers by swarm size, in twelfths of Config.Timeout.
//
// Shortening the timeout across the board is known to be a disaster here: the
// fetches that succeed are disproportionately the slow ones, so a uniform cut
// removes successes and leaves failures. What it could not do is tell those
// apart in advance — which is exactly what the seeder count does. A hash no
// large tracker has ever heard of will almost certainly ride out its budget
// and time out; one with a live swarm is worth waiting longer for.
const (
	budgetUnseeded = 4  // 1/3 — nobody is announcing this
	budgetThin     = 8  // 2/3 — a handful of seeders
	budgetHealthy  = 16 // 4/3 — popular, and likely to answer
	thinSeeders    = 5
)

// Config controls the fetcher.
type Config struct {
	// Workers is the size of the fetch worker pool. It also bounds the
	// number of concurrently active torrents in the client.
	Workers int
	// Timeout is the baseline wait for one torrent's info. The actual budget
	// scales with the request's seeder count; see timeoutFor.
	Timeout time.Duration
	// Known, when set, is asked before each fetch whether the infohash is
	// already indexed or blocked. Those fetches produce a row the store then
	// discards, so skipping them hands the worker slot back to a hash that
	// might yield something.
	Known func(hash string) bool
	// MaxFiles caps how many file entries are kept per torrent.
	MaxFiles int
	// Trackers are announce URLs appended to every magnet. Tracker announces
	// return a peer list in one round trip, where a DHT get_peers walk can
	// eat most of the fetch timeout — for well-tracked (popular) content
	// this is the difference between fetching and timing out.
	Trackers []string
	// Logger, nil for log.Default().
	Logger *log.Logger
}

// Fetcher consumes infohashes and reports successfully fetched metadata.
type Fetcher struct {
	cfg          Config
	client       *torrent.Client
	tmpDir       string
	logger       *log.Logger
	magnetSuffix string // pre-encoded &tr=... params, possibly empty

	wg     sync.WaitGroup
	cancel context.CancelFunc

	// Stats
	mu       sync.Mutex
	fetched  int64
	timedOut int64
	failed   int64
	skipped  int64
}

// NewFetcher creates a fetcher with its own torrent client. The client uses
// a temporary data dir and data download is disallowed per torrent, so no
// payload is persisted.
func NewFetcher(cfg Config) (*Fetcher, error) {
	if cfg.Workers <= 0 {
		cfg.Workers = 16
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 45 * time.Second
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = 50
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	tmpDir, err := os.MkdirTemp("", "dhtsearch-meta-*")
	if err != nil {
		return nil, err
	}
	tc := torrent.NewDefaultClientConfig()
	tc.DataDir = tmpDir
	tc.ListenPort = 0
	tc.NoUpload = true
	tc.Seed = false
	tc.NoDefaultPortForwarding = true
	client, err := torrent.NewClient(tc)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("torrent client: %w", err)
	}
	return &Fetcher{
		cfg:          cfg,
		client:       client,
		tmpDir:       tmpDir,
		logger:       logger,
		magnetSuffix: magnetTrackerParams(cfg.Trackers),
	}, nil
}

// magnetTrackerParams encodes announce URLs as magnet &tr= parameters.
func magnetTrackerParams(trackers []string) string {
	var b strings.Builder
	for _, tr := range trackers {
		tr = strings.TrimSpace(tr)
		if tr == "" {
			continue
		}
		b.WriteString("&tr=")
		b.WriteString(url.QueryEscape(tr))
	}
	return b.String()
}

// Run starts the worker pool consuming requests from in until in is closed or
// the fetcher is closed. onRecord is called (from worker goroutines) for every
// torrent whose metadata was fetched in time.
func (f *Fetcher) Run(ctx context.Context, in <-chan Request, onRecord func(Record)) {
	ctx, f.cancel = context.WithCancel(ctx)
	for i := 0; i < f.cfg.Workers; i++ {
		f.wg.Add(1)
		go f.worker(ctx, in, onRecord)
	}
}

func (f *Fetcher) worker(ctx context.Context, in <-chan Request, onRecord func(Record)) {
	defer f.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case req, ok := <-in:
			if !ok {
				return
			}
			if rec, ok := f.fetch(ctx, req); ok {
				onRecord(rec)
			}
		}
	}
}

// timeoutFor scales the fetch budget by how healthy the swarm looked when the
// scraper asked the trackers about it.
func (f *Fetcher) timeoutFor(seeders int32) time.Duration {
	switch {
	case seeders < 0:
		return f.cfg.Timeout // never scraped; no basis to adjust
	case seeders == 0:
		return f.cfg.Timeout * budgetUnseeded / 12
	case seeders < thinSeeders:
		return f.cfg.Timeout * budgetThin / 12
	default:
		return f.cfg.Timeout * budgetHealthy / 12
	}
}

// fetch adds the magnet, waits for GotInfo with a timeout and always drops
// the torrent afterwards.
func (f *Fetcher) fetch(ctx context.Context, req Request) (Record, bool) {
	hexHash := req.InfoHash
	rec := Record{InfoHash: hexHash}
	// AddMagnet panics (rather than returning an error) on a zero or
	// malformed infohash, and a panic in a worker takes the process with it.
	// The crawler already filters these, but this is the last line before an
	// untrusted value reaches the library, so check here too.
	if !validInfohash(hexHash) {
		f.count(&f.failed)
		return rec, false
	}
	// Cheap primary-key lookup before an expensive fetch: the crawler re-offers
	// hashes the index already holds as soon as its dedup ring rolls over, and
	// a blocked hash would be rejected only after the fetch had already been
	// paid for.
	if f.cfg.Known != nil && f.cfg.Known(hexHash) {
		f.count(&f.skipped)
		return rec, false
	}
	t, err := f.client.AddMagnet("magnet:?xt=urn:btih:" + hexHash + f.magnetSuffix)
	if err != nil {
		f.count(&f.failed)
		return rec, false
	}
	defer t.Drop()
	t.DisallowDataDownload()

	select {
	case <-t.GotInfo():
	case <-time.After(f.timeoutFor(req.Seeders)):
		f.count(&f.timedOut)
		return rec, false
	case <-ctx.Done():
		return rec, false
	}

	info := t.Info()
	if info == nil {
		f.count(&f.failed)
		return rec, false
	}
	rec.Name = info.BestName()
	rec.TotalSize = info.TotalLength()
	files := info.UpvertedFiles()
	rec.FileCount = len(files)
	if n := len(files); n > f.cfg.MaxFiles {
		files = files[:f.cfg.MaxFiles]
	}
	for _, fi := range files {
		rec.Files = append(rec.Files, filter.File{
			Path: fi.DisplayPath(info),
			Size: fi.Length,
		})
	}
	f.count(&f.fetched)
	return rec, true
}

// validInfohash reports whether s is a 40-character hex infohash that is not
// all zeros.
func validInfohash(s string) bool {
	if len(s) != 40 {
		return false
	}
	zero := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
		if c != '0' {
			zero = false
		}
	}
	return !zero
}

func (f *Fetcher) count(p *int64) {
	f.mu.Lock()
	*p++
	f.mu.Unlock()
}

// Stats returns the fetch outcome counters. skipped counts requests dropped
// before any network work because the store already knew the infohash.
func (f *Fetcher) Stats() (fetched, timedOut, failed, skipped int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetched, f.timedOut, f.failed, f.skipped
}

// Close stops the workers and the torrent client.
func (f *Fetcher) Close() {
	if f.cancel != nil {
		f.cancel()
	}
	f.wg.Wait()
	f.client.Close()
	os.RemoveAll(f.tmpDir)
}
