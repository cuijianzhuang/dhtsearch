// Package native is a purpose-built, thin BEP 9 metadata fetcher: an
// alternative foundation to the anacrolix/torrent-client fetcher in
// internal/metadata, selected by the META_FETCHER setting. It does
// exactly four things — announce to trackers for peers, connect to
// peers, run the BEP 10 extended handshake, and pull the BEP 9
// metadata pieces — and nothing else: no piece management, no
// storage, no DHT, no announce of our own torrents, no listener.
//
// The reason it exists is the incumbent's shape: a download client
// serializes nearly everything through one process-wide lock per
// client, and a metadata fetcher's connect/drop churn saturates that
// lock long before the network (production stall, 2026-11-20; split
// into shards by PR #33, which bounds but cannot remove the cost).
// Here there is no shared lock to saturate: a fetch is a set of
// independent peer sessions coordinated through one mutex-guarded
// attempt state, every wait is a context or a socket deadline, and a
// stuck peer costs at most its own goroutine. The lock-wedge class
// of failure is impossible by construction.
//
// The external contract is metadata.Backend, identical to the
// incumbent's: same Config in, same Record out (built through
// metadata.RecordFromInfo), same prescreen-inside-the-budget
// semantics, and the same outcome counters — fetched / timed out /
// failed / prescreen-dropped mean the same things, so the admin
// pipeline and the stall watchdog read the same quantities whichever
// foundation is active.
//
// Peer supply parity with the incumbent, and the one divergence:
// the incumbent's clients get peers from the magnet's trackers plus
// PEX; so does this fetcher (same tracker list, same announces, PEX
// consumed). Both also consume the queue's DHT peer hints
// (metadata.Item.Peers): the native fetcher seeds them into the
// attempt's pool before the announce round starts, so they take the
// first fan-out slots and race tracker/PEX peers inside the same
// budget and prescreen. The incumbent also accepts inbound connections on its
// listen socket; this fetcher has no listener, so peers that would
// have connected inbound after seeing our tracker announces are not
// reached. Outbound tracker+PEX supply is the dominant channel (the
// incumbent's fetches connect out to announced peers the same way);
// the size of the inbound-only remainder is a measured-after-
// deployment unknown, to be read off the A/B pipeline comparison.
package native

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"log"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/utp"

	"dhtsearch/server/internal/metadata"
)

// maxPeerFanout bounds the connection attempts one fetch keeps in
// flight. A hash can have hundreds of known peers; racing a handful
// and letting the first verified metadata win keeps the socket cost
// of a fetch flat while covering the common case (the first peers
// are dead, a later one serves).
const maxPeerFanout = 4

// Fetcher is the native foundation's metadata.Backend.
type Fetcher struct {
	cfg    metadata.Config
	logger *log.Logger
	peerID [20]byte

	src       peerSource
	tcpDialer *net.Dialer
	utpSock   *utp.Socket // nil when uTP is unavailable: TCP only

	wg     sync.WaitGroup
	cancel context.CancelFunc

	mu               sync.Mutex
	fetched          int64
	timedOut         int64
	failed           int64
	prescreenDropped int64
	hintItems        int64 // fetches that carried at least one DHT peer hint
	hintFetched      int64 // of those, successes with a hint peer connected during the attempt

	// Per-source attribution of the same outcomes counted above,
	// split by the fetched items' discovery Source.
	srcStats metadata.SourceTracker
}

var _ metadata.Backend = (*Fetcher)(nil)

// New builds a native fetcher from the same Config the anacrolix
// fetcher takes. Config.Shards is meaningless here (there are no
// clients to shard) and is ignored.
func New(cfg metadata.Config) (*Fetcher, error) {
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
		cfg.PrescreenTimeout = metadata.DefaultPrescreenTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	var peerID [20]byte
	copy(peerID[:], "-DS0100-")
	if _, err := rand.Read(peerID[8:]); err != nil {
		return nil, err
	}
	f := &Fetcher{
		cfg:       cfg,
		logger:    logger,
		peerID:    peerID,
		src:       newTrackerSource(cfg.Trackers, peerID),
		tcpDialer: &net.Dialer{},
	}
	// One shared uTP socket for all dials. If it cannot be created
	// the fetcher degrades to TCP only rather than failing: TCP
	// reaches a large share of swarms, and a hard failure here would
	// take the whole pipeline down over one transport.
	if sock, err := utp.NewSocket("udp", ":0"); err != nil {
		logger.Printf("native metadata: uTP socket unavailable, TCP only: %v", err)
	} else {
		f.utpSock = sock
	}
	return f, nil
}

// Run consumes Items from in until in closes or the fetcher
// is closed. Mirrors the incumbent's worker pool shape; only the
// item's Hash and Source (the attribution tag) are consumed.
func (f *Fetcher) Run(ctx context.Context, in <-chan metadata.Item, onRecord func(metadata.Record)) {
	ctx, f.cancel = context.WithCancel(ctx)
	for i := 0; i < f.cfg.Workers; i++ {
		f.wg.Add(1)
		go f.worker(ctx, in, onRecord)
	}
}

func (f *Fetcher) worker(ctx context.Context, in <-chan metadata.Item, onRecord func(metadata.Record)) {
	defer f.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case it, ok := <-in:
			if !ok {
				return
			}
			if rec, ok := f.fetch(ctx, it.Hash, it.Source); ok {
				onRecord(rec)
			}
		}
	}
}

// RunQueue is Run with the priority Queue as the input.
func (f *Fetcher) RunQueue(ctx context.Context, q *metadata.Queue, onRecord func(metadata.Record)) {
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
				if rec, ok := f.fetchFull(ctx, it.Hash, it.Source, it.Peers); ok {
					onRecord(rec)
				}
			}
		}()
	}
}

// fetch runs one infohash through an attempt and folds the outcome
// into the stats counters, with the incumbent's meanings:
//   - fetched: verified metadata delivered.
//   - prescreenDropped: prescreen on, and no peer connection was
//     established within the prescreen window.
//   - timedOut: the total budget expired without verified metadata.
//   - failed: the hash was invalid, or the swarm provably lied — a
//     completed metadata blob whose SHA-1 is not the requested
//     infohash, or a peer claiming an impossible metadata size.
func (f *Fetcher) fetch(ctx context.Context, hexHash string, src metadata.Source) (metadata.Record, bool) {
	return f.fetchFull(ctx, hexHash, src, nil)
}

// fetchPeers is fetch with DHT peer hints (metadata.Item.Peers) and
// no explicit source (attributed to SourceDHT, the hints' origin).
func (f *Fetcher) fetchPeers(ctx context.Context, hexHash string, peers []netip.AddrPort) (metadata.Record, bool) {
	return f.fetchFull(ctx, hexHash, metadata.SourceDHT, peers)
}

// fetchFull runs one attempt. peers carries DHT peer hints:
// addresses the crawler already knows are in this swarm, seeded
// into the attempt's pool before the tracker announce round starts
// so they race the tracker-supplied peers inside the same timeout
// and prescreen budget — the incumbent's warm start, in native
// terms. A hint is just an address: it goes through the same dial,
// handshake and SHA-1 verification as any tracker or PEX candidate,
// and a dead hint costs nothing but a failed dial in parallel with
// the tracker path.
//
// src is the item's discovery source: the attempt and its outcome
// are attributed to it (see metadata.SourceTracker), identically
// to the incumbent.
func (f *Fetcher) fetchFull(ctx context.Context, hexHash string, src metadata.Source, peers []netip.AddrPort) (metadata.Record, bool) {
	f.srcStats.Attempt(src)
	rec := metadata.Record{InfoHash: hexHash}
	if !validInfohash(hexHash) {
		f.finish(src, hexHash, metadata.OutcomeFailed)
		return rec, false
	}
	raw, err := hex.DecodeString(hexHash)
	if err != nil || len(raw) != 20 {
		f.finish(src, hexHash, metadata.OutcomeFailed)
		return rec, false
	}
	var ih [20]byte
	copy(ih[:], raw)

	// Normalise hints to the pool's string form. Unmapping matches
	// the incumbent's attribution (a 4-in-6 hint is the same peer as
	// the IPv4 address trackers report in compact form).
	var hintAddrs []string
	for _, ap := range peers {
		if !ap.IsValid() {
			continue
		}
		hintAddrs = append(hintAddrs, netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()).String())
	}
	if len(peers) > 0 {
		f.count(&f.hintItems)
	}

	timeout := f.cfg.Timeout
	if f.cfg.TimeoutFunc != nil {
		if d := f.cfg.TimeoutFunc(); d > 0 {
			timeout = d
		}
	}
	a := newAttempt(f, ih, hexHash)
	a.addHints(hintAddrs)
	outRec, outcome := a.run(ctx, timeout, f.prescreenWindow(timeout))
	switch outcome {
	case outcomeSuccess:
		if len(peers) > 0 && a.hintWasConnected() {
			f.count(&f.hintFetched)
		}
		f.finish(src, hexHash, metadata.OutcomeFetched)
		return outRec, true
	case outcomePrescreened:
		f.finish(src, hexHash, metadata.OutcomePrescreened)
	case outcomeTimeout:
		f.finish(src, hexHash, metadata.OutcomeTimedOut)
	case outcomeFailed:
		f.finish(src, hexHash, metadata.OutcomeFailed)
	case outcomeAborted:
	}
	return rec, false
}

// finish records a fetch's terminal outcome in the fetcher's global
// counters, in the per-source attribution, and in the ResultFunc
// report — the three always move together, mirroring the
// incumbent's finish.
func (f *Fetcher) finish(src metadata.Source, hash string, outcome metadata.FetchOutcome) {
	switch outcome {
	case metadata.OutcomeFetched:
		f.count(&f.fetched)
	case metadata.OutcomeTimedOut:
		f.count(&f.timedOut)
	case metadata.OutcomeFailed:
		f.count(&f.failed)
	case metadata.OutcomePrescreened:
		f.count(&f.prescreenDropped)
	}
	f.srcStats.Outcome(src, outcome)
	f.report(hash, outcome)
}

// report delivers a fetch outcome to Config.ResultFunc, when set,
// mirroring the incumbent fetcher's contract (metadata.FetchOutcome).
// Aborted attempts (context cancelled) are never reported, so a
// shutdown does not push live hashes into the failure backoff.
func (f *Fetcher) report(hash string, outcome metadata.FetchOutcome) {
	if f.cfg.ResultFunc != nil {
		f.cfg.ResultFunc(hash, outcome)
	}
}

// prescreenWindow resolves the prescreen phase length for one fetch
// with the given total timeout, exactly as the incumbent does: 0
// when prescreening is off, or when the window would cover the whole
// budget (it could never fire in time).
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
		d = metadata.DefaultPrescreenTimeout
	}
	if d >= timeout {
		return 0
	}
	return d
}

// recordFromRaw verifies a completed metadata blob against the
// requested infohash and builds its Record. The SHA-1 check is the
// fetcher's core correctness rule: bytes that do not hash to the
// requested infohash are never turned into a Record.
func (f *Fetcher) recordFromRaw(ih [20]byte, hexHash string, raw []byte) (metadata.Record, bool) {
	if sha1.Sum(raw) != ih {
		return metadata.Record{}, false
	}
	var info metainfo.Info
	if err := bencode.Unmarshal(raw, &info); err != nil {
		return metadata.Record{}, false
	}
	return metadata.RecordFromInfo(&info, hexHash, f.cfg.MaxFiles), true
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
// abandoned early.
func (f *Fetcher) PrescreenDropped() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prescreenDropped
}

// PeerHintStats returns the peer-hint counters, mirroring
// metadata.Fetcher.PeerHintStats: items is how many fetches carried
// at least one DHT peer hint (Item.Peers); fetched is how many of
// those succeeded with a hint peer connected during the attempt
// (see attempt.hintWasConnected for exactly what that does and does
// not claim). It is an optional Backend capability: the stats
// mirror in main.go type-asserts for it, so the peer_hint_items /
// peer_hint_fetched stats move under this backend exactly as they
// do under the incumbent.
func (f *Fetcher) PeerHintStats() (items, fetched int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hintItems, f.hintFetched
}

// SourceStats returns the per-source fetch counters, identically
// attributed to the incumbent's. Part of the metadata.Backend
// contract.
func (f *Fetcher) SourceStats() []metadata.FetchSourceStats {
	return f.srcStats.Stats()
}

// Close stops the workers and releases the uTP socket.
func (f *Fetcher) Close() {
	if f.cancel != nil {
		f.cancel()
	}
	f.wg.Wait()
	if f.utpSock != nil {
		f.utpSock.Close()
	}
}

// validInfohash reports whether s is a 40-character hex infohash
// that is not all zeros. It duplicates metadata's unexported check:
// the value is untrusted input at the last line before the network,
// and both foundations must reject exactly the same strings.
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

// --- attempt: one fetch's coordination state ---

type attemptOutcome int

const (
	outcomeSuccess attemptOutcome = iota
	outcomeTimeout
	outcomePrescreened
	outcomeFailed
	outcomeAborted
)

// attempt coordinates one fetch: the peer pool, the fan-out of
// sessions racing to complete the metadata, and the outcome. All
// mutable state sits behind mu; sessions report through the small
// callback methods, each of which signals notify so the manager loop
// in run can re-evaluate. Nothing blocks while holding mu.
type attempt struct {
	f       *Fetcher
	ih      [20]byte
	hexHash string

	mu           sync.Mutex
	notify       chan struct{}
	pool         []string
	queued       map[string]struct{}
	tried        map[string]struct{}
	active       int
	connected    bool
	poisoned     bool
	announceDone bool
	completed    [][]byte
	conns        map[net.Conn]struct{}
	hints        map[string]struct{} // normalised hint addresses for this fetch
	hintConn     bool                // a session to a hint address completed the handshake
}

func newAttempt(f *Fetcher, ih [20]byte, hexHash string) *attempt {
	return &attempt{
		f:       f,
		ih:      ih,
		hexHash: hexHash,
		notify:  make(chan struct{}, 1),
		queued:  make(map[string]struct{}),
		tried:   make(map[string]struct{}),
		conns:   make(map[net.Conn]struct{}),
	}
}

func (a *attempt) signal() {
	select {
	case a.notify <- struct{}{}:
	default:
	}
}

// addPeers offers candidate addresses (tracker results, PEX
// additions) to the pool. Addresses already tried or queued are
// dropped.
func (a *attempt) addPeers(addrs []string) {
	a.mu.Lock()
	changed := false
	for _, addr := range addrs {
		if _, ok := a.tried[addr]; ok {
			continue
		}
		if _, ok := a.queued[addr]; ok {
			continue
		}
		a.queued[addr] = struct{}{}
		a.pool = append(a.pool, addr)
		changed = true
	}
	a.mu.Unlock()
	if changed {
		a.signal()
	}
}

// addHints records the fetch's DHT peer hints and seeds them into
// the pool. Called once, before run, so the hints occupy the first
// fan-out slots and race the tracker announce round rather than
// queueing behind it. A hint address a tracker later returns is
// deduplicated by addPeers like any repeat.
func (a *attempt) addHints(addrs []string) {
	if len(addrs) == 0 {
		return
	}
	a.mu.Lock()
	a.hints = make(map[string]struct{}, len(addrs))
	for _, addr := range addrs {
		a.hints[addr] = struct{}{}
	}
	a.mu.Unlock()
	a.addPeers(addrs)
}

func (a *attempt) sessionConnected(addr string) {
	a.mu.Lock()
	a.connected = true
	if _, ok := a.hints[addr]; ok {
		a.hintConn = true
	}
	a.mu.Unlock()
	a.signal()
}

// hintWasConnected reports whether any session to one of this
// fetch's hint addresses completed the BitTorrent handshake during
// the attempt. It is the attribution behind the hintFetched
// counter, with the same deliberately narrow claim as the
// incumbent's hintPeerConnected: it proves a hinted address was
// connected for this torrent — not that the hint peer delivered
// the decisive piece (a tracker or PEX peer at the same address
// counts, exactly as there). One honest difference of mechanism:
// native sessions are single-purpose and close as soon as they
// have served, so "connected at the moment of completion" is not
// observable here; handshake-during-the-attempt is the equivalent
// signal, and it is still a lower bound on the hints' usefulness,
// not a causal claim.
func (a *attempt) hintWasConnected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hintConn
}

// complete hands a fully assembled (still unverified) metadata blob
// to the manager.
func (a *attempt) complete(raw []byte) {
	a.mu.Lock()
	a.completed = append(a.completed, raw)
	a.mu.Unlock()
	a.signal()
}

// markPoisoned records that a peer provably lied about this torrent
// (impossible metadata size, size self-contradiction, or — via the
// manager's verification — a completed blob that does not hash to
// the requested infohash). One lying peer does not fail the fetch
// while honest candidates remain; it decides the outcome only once
// the supply is exhausted.
func (a *attempt) markPoisoned() {
	a.mu.Lock()
	a.poisoned = true
	a.mu.Unlock()
	a.signal()
}

func (a *attempt) sessionEnded() {
	a.mu.Lock()
	a.active--
	a.mu.Unlock()
	a.signal()
}

func (a *attempt) registerConn(conn net.Conn) {
	a.mu.Lock()
	a.conns[conn] = struct{}{}
	a.mu.Unlock()
}

func (a *attempt) unregisterConn(conn net.Conn) {
	a.mu.Lock()
	delete(a.conns, conn)
	a.mu.Unlock()
}

// closeConns drops every live session connection; called when the
// attempt resolves so racing losers stop at once.
func (a *attempt) closeConns() {
	a.mu.Lock()
	conns := a.conns
	a.conns = make(map[net.Conn]struct{})
	a.mu.Unlock()
	for conn := range conns {
		conn.Close()
	}
}

// run drives the attempt to an outcome under the total budget. The
// prescreen window is part of the budget, exactly as the incumbent:
// it only shortens the wait for hashes nobody connects to.
func (a *attempt) run(ctx context.Context, timeout, prescreen time.Duration) (metadata.Record, attemptOutcome) {
	budgetCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer a.closeConns()

	peerCh := make(chan string, 256)
	go func() {
		a.f.src.Peers(budgetCtx, a.ih, peerCh)
		close(peerCh)
	}()

	var preC <-chan time.Time
	if prescreen > 0 {
		pre := time.NewTimer(prescreen)
		defer pre.Stop()
		preC = pre.C
	}
	prescreenFired := false

	for {
		// Verify any completed blobs first: a success ends the
		// attempt immediately, a mismatch poisons it.
		for {
			a.mu.Lock()
			if len(a.completed) == 0 {
				a.mu.Unlock()
				break
			}
			raw := a.completed[0]
			a.completed = a.completed[1:]
			a.mu.Unlock()
			if rec, ok := a.f.recordFromRaw(a.ih, a.hexHash, raw); ok {
				return rec, outcomeSuccess
			}
			a.markPoisoned()
		}

		// Spend free fan-out slots on untried peers.
		a.mu.Lock()
		for a.active < maxPeerFanout && len(a.pool) > 0 {
			addr := a.pool[0]
			a.pool = a.pool[1:]
			delete(a.queued, addr)
			a.tried[addr] = struct{}{}
			a.active++
			go a.runSession(budgetCtx, addr)
		}
		// Terminal states that need no more waiting: the supply is
		// exhausted (announce round over, no session running, pool
		// empty) and the swarm provably lied — fail now instead of
		// riding out the budget. Every other exhausted shape keeps
		// waiting for the deadline, matching the incumbent, whose
		// torrent would sit in the client until its timeout either
		// way.
		drained := a.announceDone && a.active == 0 && len(a.pool) == 0
		poisoned := a.poisoned
		connected := a.connected
		a.mu.Unlock()
		if drained && poisoned {
			return metadata.Record{}, outcomeFailed
		}
		if prescreenFired && !connected {
			return metadata.Record{}, outcomePrescreened
		}

		select {
		case addr, ok := <-peerCh:
			if !ok {
				a.mu.Lock()
				a.announceDone = true
				a.mu.Unlock()
				a.signal()
				// peerCh is closed; nil it out so the select does
				// not spin on it, and loop to re-evaluate.
				peerCh = nil
				continue
			}
			a.addPeers([]string{addr})
		case <-a.notify:
		case <-preC:
			prescreenFired = true
			preC = nil
		case <-budgetCtx.Done():
			if ctx.Err() != nil {
				return metadata.Record{}, outcomeAborted
			}
			return metadata.Record{}, outcomeTimeout
		}
	}
}
