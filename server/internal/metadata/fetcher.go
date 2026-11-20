// Package metadata fetches torrent metainfo (BEP 9) for discovered
// infohashes with a bounded worker pool, without downloading payload data.
package metadata

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/storage/disabled"

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

// Config controls the fetcher.
type Config struct {
	// Workers is the size of the fetch worker pool. It also bounds the
	// number of concurrently active torrents across all client shards.
	Workers int
	// Shards is the number of independent torrent clients the fetcher
	// spreads its torrents across. anacrolix serializes nearly every
	// operation through one process-wide lock per client, and a
	// metadata fetcher's Add/Drop/connect churn saturates that lock
	// long before it saturates the network (production, 2026-11-20:
	// 125 goroutines queued on Client.lock, ingest at ~61/10m against
	// a ~330 baseline). Each hash is routed to one shard by its first
	// byte, so concurrent torrents per lock fall to roughly
	// Workers/Shards — 16 at the defaults (128 workers, 8 shards)
	// instead of 128. <= 0 selects DefaultShards; the effective count
	// is clamped to Workers (more clients than workers only multiplies
	// per-client overhead).
	Shards int
	// Timeout is how long to wait for one torrent's info before giving up.
	Timeout time.Duration
	// TimeoutFunc, when non-nil, supplies the timeout per fetch,
	// overriding Timeout — the settings system uses it so META_TIMEOUT
	// changes apply without rebuilding the fetcher.
	TimeoutFunc func() time.Duration
	// PrescreenEnabled turns on the early-abandon phase: a fetch first
	// waits at most PrescreenTimeout for any peer connection to be
	// established, and gives up then if none was — instead of holding
	// the worker slot for the whole Timeout on a hash nobody serves.
	// The prescreen window is part of the Timeout budget, not added to
	// it: a torrent that connects keeps the full remaining budget, so
	// the empirically calibrated 45s total is unchanged for live swarms.
	PrescreenEnabled bool
	// PrescreenTimeout bounds the prescreen phase. <= 0 selects
	// DefaultPrescreenTimeout. A window >= the fetch timeout disables
	// prescreening for that fetch (it could never fire in time).
	PrescreenTimeout time.Duration
	// PrescreenEnabledFunc / PrescreenTimeoutFunc, when non-nil,
	// override the static fields per fetch — the settings system uses
	// them so META_PRESCREEN_* changes apply live.
	PrescreenEnabledFunc func() bool
	PrescreenTimeoutFunc func() time.Duration
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
	clients      []*torrent.Client // shards; see Config.Shards
	logger       *log.Logger
	magnetSuffix string // pre-encoded &tr=... params, possibly empty

	wg     sync.WaitGroup
	cancel context.CancelFunc

	// Stats
	mu               sync.Mutex
	fetched          int64
	timedOut         int64
	failed           int64
	prescreenDropped int64
}

// DefaultPrescreenTimeout is the prescreen window used when none is
// configured. Ten seconds is deliberately conservative: tracker
// announces (appended to every magnet) answer with peer lists in one
// round trip, and a BitTorrent handshake completes in about a second
// once an address is known, so a live swarm virtually always shows a
// connection well inside the window. What the window mostly waits on
// is the DHT get_peers walk for trackerless hashes — the slow tail the
// 45s total timeout was calibrated on — so the drop rule additionally
// requires that no peer ever connected during the whole window, and
// the window is a settings knob (META_PRESCREEN_TIMEOUT) for operators
// who measure a different tail.
const DefaultPrescreenTimeout = 10 * time.Second

// DefaultShards is the torrent-client shard count used when none is
// configured. Eight shards take the default 128-worker pool from 128
// concurrent torrents per client lock to ~16, at the cost of eight
// (DHT-less, storage-less) clients' fixed overhead.
const DefaultShards = 8

// NewFetcher creates a fetcher with its own torrent clients (shards,
// see Config.Shards). The clients use disabled storage and disallow
// payload transfers before adding each torrent.
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
	if cfg.PrescreenTimeout <= 0 {
		cfg.PrescreenTimeout = DefaultPrescreenTimeout
	}
	shards := cfg.Shards
	if shards <= 0 {
		shards = DefaultShards
	}
	if shards > cfg.Workers {
		shards = cfg.Workers
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	clients := make([]*torrent.Client, 0, shards)
	for i := 0; i < shards; i++ {
		client, err := torrent.NewClient(metadataClientConfig())
		if err != nil {
			for _, c := range clients {
				c.Close()
			}
			return nil, fmt.Errorf("torrent client shard %d: %w", i, err)
		}
		clients = append(clients, client)
	}
	return &Fetcher{
		cfg:          cfg,
		clients:      clients,
		logger:       logger,
		magnetSuffix: magnetTrackerParams(cfg.Trackers),
	}, nil
}

// shardIndex routes a hex infohash to a client shard by the hash's
// first byte. It is a pure function of the hash, so the same hash
// always lands on the same client — per-client AddTorrentSpec dedup
// keeps its meaning, and a hash's torrents never straddle two locks.
// Malformed input (fetch validates before routing, so this is only a
// belt-and-braces fallback) routes to shard 0.
func shardIndex(hexHash string, shards int) int {
	if shards <= 1 || len(hexHash) < 2 {
		return 0
	}
	hi, okHi := hexNibble(hexHash[0])
	lo, okLo := hexNibble(hexHash[1])
	if !okHi || !okLo {
		return 0
	}
	return int(hi<<4|lo) % shards
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// clientFor returns the shard client responsible for hexHash.
func (f *Fetcher) clientFor(hexHash string) *torrent.Client {
	return f.clients[shardIndex(hexHash, len(f.clients))]
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

// Run starts the worker pool consuming hex infohashes from in until in is
// closed or the fetcher is closed. onRecord is called (from worker
// goroutines) for every torrent whose metadata was fetched in time.
func (f *Fetcher) Run(ctx context.Context, in <-chan string, onRecord func(Record)) {
	ctx, f.cancel = context.WithCancel(ctx)
	for i := 0; i < f.cfg.Workers; i++ {
		f.wg.Add(1)
		go f.worker(ctx, in, onRecord)
	}
}

func (f *Fetcher) worker(ctx context.Context, in <-chan string, onRecord func(Record)) {
	defer f.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case hexHash, ok := <-in:
			if !ok {
				return
			}
			if rec, ok := f.fetch(ctx, hexHash); ok {
				onRecord(rec)
			}
		}
	}
}

// RunQueue is Run with a priority Queue as the input instead of a
// channel: workers pop the highest-priority waiting item (see Queue
// for the ordering). It returns when ctx ends; items left in the queue
// are simply abandoned, exactly like an undrained channel at shutdown.
func (f *Fetcher) RunQueue(ctx context.Context, q *Queue, onRecord func(Record)) {
	ctx, f.cancel = context.WithCancel(ctx)
	for i := 0; i < f.cfg.Workers; i++ {
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			for {
				it, ok := q.Pop(ctx)
				if !ok {
					return
				}
				if rec, ok := f.fetch(ctx, it.Hash); ok {
					onRecord(rec)
				}
			}
		}()
	}
}

// fetch adds the magnet, waits for GotInfo with a timeout and always drops
// the torrent afterwards.
func (f *Fetcher) fetch(ctx context.Context, hexHash string) (Record, bool) {
	rec := Record{InfoHash: hexHash}
	// AddMagnet panics (rather than returning an error) on a zero or
	// malformed infohash, and a panic in a worker takes the process with it.
	// The crawler already filters these, but this is the last line before an
	// untrusted value reaches the library, so check here too.
	if !validInfohash(hexHash) {
		f.count(&f.failed)
		return rec, false
	}
	spec, err := torrent.TorrentSpecFromMagnetUri("magnet:?xt=urn:btih:" + hexHash + f.magnetSuffix)
	if err != nil {
		f.count(&f.failed)
		return rec, false
	}
	spec.DisallowDataDownload = true
	spec.DisallowDataUpload = true
	spec.DisableInitialPieceCheck = true
	// AddTorrentSpec takes the shard client's process-wide lock and
	// has no context variant: if that lock is ever wedged (see the
	// Drop call below), this call parks the worker with no way out.
	// The production wedge was congestion collapse of that single
	// lock, not one stuck holder: per-torrent DHT announcers (now
	// disabled in metadataClientConfig) plus the fetcher's own
	// Add/Drop/connect churn at 128 concurrent torrents kept over a
	// hundred goroutines queued on it. Sharding (clientFor) bounds
	// the blast radius — one shard's lock contention stalls only its
	// ~Workers/Shards share of the pool — but the lock exposure of
	// AddTorrentSpec, t.Drop and Client.Close themselves remains, so
	// the pipeline watchdog (internal/watchdog) stays as the
	// backstop: it detects a resulting stall or crawl and exits the
	// process for systemd to restart.
	t, _, err := f.clientFor(hexHash).AddTorrentSpec(spec)
	if err != nil {
		f.count(&f.failed)
		return rec, false
	}
	defer t.Drop()

	timeout := f.cfg.Timeout
	if f.cfg.TimeoutFunc != nil {
		if d := f.cfg.TimeoutFunc(); d > 0 {
			timeout = d
		}
	}
	switch waitForInfo(ctx, torrentInfoSource{t}, timeout, f.prescreenWindow(timeout)) {
	case waitGotInfo:
	case waitPrescreened:
		f.count(&f.prescreenDropped)
		return rec, false
	case waitTimedOut:
		f.count(&f.timedOut)
		return rec, false
	case waitAborted:
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

// prescreenWindow resolves the prescreen phase length for one fetch
// with the given total timeout: 0 when prescreening is off, when the
// window would cover the whole budget (it could never fire in time),
// or when the settings funcs say so.
func (f *Fetcher) prescreenWindow(timeout time.Duration) time.Duration {
	enabled := f.cfg.PrescreenEnabled
	if f.cfg.PrescreenEnabledFunc != nil {
		enabled = f.cfg.PrescreenEnabledFunc()
	}
	if !enabled {
		return 0
	}
	d := f.cfg.PrescreenTimeout
	if f.cfg.PrescreenTimeoutFunc != nil {
		if v := f.cfg.PrescreenTimeoutFunc(); v > 0 {
			d = v
		}
	}
	if d <= 0 {
		d = DefaultPrescreenTimeout
	}
	if d >= timeout {
		return 0
	}
	return d
}

// waitOutcome is how a metadata wait ended.
type waitOutcome int

const (
	waitGotInfo waitOutcome = iota
	waitTimedOut
	// waitPrescreened: the prescreen window closed without a single
	// peer connection ever being established — the hash is abandoned
	// early instead of riding out the full timeout.
	waitPrescreened
	waitAborted // ctx ended
)

// infoSource abstracts the two things the fetch wait needs from a
// torrent, so the prescreen logic is testable without a live client.
type infoSource interface {
	GotInfo() <-chan struct{}
	// ActivePeers is the number of established peer connections.
	ActivePeers() int
}

type torrentInfoSource struct{ t *torrent.Torrent }

func (s torrentInfoSource) GotInfo() <-chan struct{} { return s.t.GotInfo() }
func (s torrentInfoSource) ActivePeers() int         { return s.t.Stats().ActivePeers }

// prescreenPollInterval is how often the prescreen phase checks for a
// peer connection. Polling Stats takes the client read lock briefly;
// at 100ms across the worker pool that is noise next to the network
// waits it replaces.
const prescreenPollInterval = 100 * time.Millisecond

// waitForInfo waits for a torrent's info with an optional prescreen
// phase. The prescreen window is part of the total timeout budget: a
// torrent whose peer connects during the window keeps whatever budget
// remains, so the total wait for live swarms is exactly timeout — the
// prescreen only shortens the wait for hashes nobody connects to.
func waitForInfo(ctx context.Context, src infoSource, timeout, prescreen time.Duration) waitOutcome {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	if prescreen <= 0 || prescreen >= timeout {
		select {
		case <-src.GotInfo():
			return waitGotInfo
		case <-deadline.C:
			return waitTimedOut
		case <-ctx.Done():
			return waitAborted
		}
	}

	// Phase 1: prescreen. Info may still arrive here (a fast swarm
	// skips the phase entirely); the phase passes the moment any peer
	// connection is established.
	//
	// ActivePeers() (Torrent.Stats) takes the torrent client's read
	// lock, and the client lock is process-wide within its shard:
	// one wedged holder (Torrent.Drop waits on a WaitGroup while
	// holding the write lock) parks every later lock call on that
	// shard indefinitely. Calling it inline from the poll case below
	// would park this wait with it — the select cannot fire while a
	// case body is blocked — so the fetch's deadline and ctx would
	// stop applying. Sample it on a helper goroutine instead and
	// consume readings over a channel: the wait keeps its own exits
	// whatever the sampler is stuck in. A sampler stuck in the
	// library leaks until its call returns; at most one exists per
	// in-flight fetch. The announce traffic that wedged the lock in
	// production is now off (see metadataClientConfig) and the load
	// is sharded across clients; the pipeline watchdog
	// (internal/watchdog) remains the backstop for any other park.
	peers := make(chan int, 1)
	stopPoll := make(chan struct{})
	defer close(stopPoll)
	go func() {
		poll := time.NewTicker(prescreenPollInterval)
		defer poll.Stop()
		for {
			select {
			case <-stopPoll:
				return
			case <-poll.C:
			}
			n := src.ActivePeers()
			select {
			case peers <- n:
			default: // a reading is already queued; freshness doesn't matter
			}
		}
	}()

	pre := time.NewTimer(prescreen)
	defer pre.Stop()
	connected := false
phase1:
	for !connected {
		select {
		case <-src.GotInfo():
			return waitGotInfo
		case <-deadline.C:
			return waitTimedOut
		case <-ctx.Done():
			return waitAborted
		case <-pre.C:
			break phase1
		case n := <-peers:
			connected = n > 0
		}
	}
	if !connected {
		// Boundary re-check: a peer that connected in the same instant
		// the window closed counts as connected. Use the sampler's
		// latest reading rather than a fresh library call (which
		// could park — see above).
		select {
		case <-src.GotInfo():
			return waitGotInfo
		default:
		}
		select {
		case n := <-peers:
			connected = n > 0
		default:
		}
	}
	if !connected {
		return waitPrescreened
	}

	// Phase 2: at least one peer is connected; wait out the rest of
	// the budget for it (or another peer) to deliver the metadata.
	select {
	case <-src.GotInfo():
		return waitGotInfo
	case <-deadline.C:
		return waitTimedOut
	case <-ctx.Done():
		return waitAborted
	}
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

// Stats returns fetched/timed-out/failed counters.
func (f *Fetcher) Stats() (fetched, timedOut, failed int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetched, f.timedOut, f.failed
}

// PrescreenDropped returns how many fetches the prescreen phase
// abandoned early (no peer connection within the prescreen window).
// Kept separate from Stats so existing callers keep their shape.
func (f *Fetcher) PrescreenDropped() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prescreenDropped
}

// Close stops the workers and the torrent clients.
func (f *Fetcher) Close() {
	if f.cancel != nil {
		f.cancel()
	}
	f.wg.Wait()
	for _, c := range f.clients {
		c.Close()
	}
}

// metadataClientConfig never opens payload paths supplied by untrusted peers.
func metadataClientConfig() *torrent.ClientConfig {
	tc := torrent.NewDefaultClientConfig()
	tc.ListenPort = 0
	tc.NoUpload = true
	tc.Seed = false
	// Never announce our torrents to the DHT. The default (true) spawns
	// a dhtAnnouncer goroutine per torrent per DHT server, each running
	// get_peers/announce traversals timeboxed at 15 minutes and
	// repeatedly taking the client's process-wide lock. A metadata-only
	// fetcher gains nothing from announcing itself as a peer — it
	// disallows data transfer anyway — and at this fetcher's churn
	// (128 workers, thousands of torrents a day) the announce machinery
	// is what wedged the client lock in production on 2026-11-20:
	// ingest collapsed to ~45 fetches/10m against a ~330 baseline
	// while dozens of workers queued on Client.lock behind announce
	// and connection goroutines. Turning it off also removes the
	// torrent client's DHT get_peers (in v1.61 the announcer is the
	// only caller of Announce with getPeers=true), so hashes whose
	// swarm is listed on none of the configured trackers lose their
	// peer source; tracker announces and PEX are unaffected. That
	// trade is deliberate: an unmeasured slow tail against a total
	// pipeline wedge.
	tc.PeriodicallyAnnounceTorrentsToDht = false
	// No DHT servers in the fetcher clients at all. With the periodic
	// announcer off, nothing in the client consumes them: in v1.61
	// the per-torrent dhtAnnouncer is the only spawner of
	// AnnounceToDht (the client's only get_peers path), and the
	// remaining DHT-server uses are routing-table upkeep for that
	// same dead path — AddNode/Ping when a peer handshake carries a
	// DHT port. Peers reach a fetch through the magnet's trackers
	// and PEX instead. The crawler's DHT is a separate anacrolix/dht
	// server (internal/crawler) and is unaffected. Dropping the
	// servers also keeps every shard client cheap: no extra UDP
	// sockets, routing tables or traversal goroutines multiplied by
	// the shard count.
	tc.NoDHT = true
	tc.NoDefaultPortForwarding = true
	tc.DefaultStorage = metadataStorage{}
	tc.Callbacks.ReadMessage = ignorePayloadAvailability
	return tc
}

// v1.61.0 can count an out-of-range pre-metadata bitfield as a complete
// peer, then truncate the bitfield without balancing relative availability.
// Drop subsequently panics. Payload availability is irrelevant to BEP 9;
// skip these messages before the library updates its piece counters. Leave
// extension messages (including metadata and PEX) and connection control intact.
func ignorePayloadAvailability(_ *torrent.PeerConn, msg *pp.Message) {
	switch msg.Type {
	case pp.Have, pp.Bitfield, pp.HaveAll, pp.HaveNone, pp.AllowedFast, pp.Suggest:
		msg.Keepalive = true
	}
}

// The dependency's disabled.Client has the old OpenTorrent signature. Adapt
// its piece implementation without a filesystem or a piece-completion DB.
type metadataStorage struct{}

func (metadataStorage) OpenTorrent(context.Context, *metainfo.Info, metainfo.Hash) (storage.TorrentImpl, error) {
	return storage.TorrentImpl{Piece: func(metainfo.Piece) storage.PieceImpl {
		return disabled.Piece{}
	}}, nil
}
