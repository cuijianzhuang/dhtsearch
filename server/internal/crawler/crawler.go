// Package crawler runs a DHT node that passively observes get_peers,
// announce_peer and sample_infohashes traffic (BEP 5 / BEP 51) to discover
// infohashes, and actively queries known nodes to expand coverage.
//
// A crawler can run several DHT identities at once (Config.Identities).
// One identity only ever samples the neighbourhood of its own routing
// table, so a single node ID caps BEP 51 coverage no matter how many
// sampler workers it runs; extra identities — each with its own node
// ID, routing table and node-quality tracker — sit in different parts
// of the ID space and widen the net. All identities feed the same
// deduplicated harvest channel. With Config.IPv6Enabled an identity
// additionally runs an IPv6 DHT server (BEP 32) on the same port
// number, so swarms that only exist on the IPv6 DHT become visible
// through the same pipeline.
package crawler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/int160"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/torrent/metainfo"
)

// bootstrapNodes are well-known public DHT routers.
var bootstrapNodes = []string{
	"router.bittorrent.com:6881",
	"router.utorrent.com:6881",
	"dht.transmissionbt.com:6881",
	"dht.aelitis.com:6881",
	"dht.libtorrent.org:25401",
}

const (
	seenCapacity  = 1 << 18 // ~260k infohashes remembered for dedup
	knownCapacity = 256     // recent infohashes used for active queries
	channelSize   = 4096

	// Sampling. Discovery is dominated by how many distinct nodes we can ask
	// for samples, so the samplers run concurrently over a queue of nodes
	// rather than probing one node on a timer.
	defaultSamplers = 32
	sampleTimeout   = 10 * time.Second
	nodeQueueSize   = 8192
	nodeTrackMax    = 1 << 16 // addresses remembered for resample scheduling

	// maxIdentities bounds Config.Identities. Each identity is a full
	// DHT server with its own socket and table maintenance, so the cap
	// keeps a typo'd setting from turning into hundreds of servers;
	// 16 already covers the ID space far past the point of diminishing
	// returns for a single host.
	maxIdentities = 16

	// BEP 51 lets a node advertise how often its sample set is worth
	// re-reading. Clamp it: some nodes report absurd values, and a node we
	// never revisit is a node whose new torrents we never see.
	defaultResample = 15 * time.Minute
	minResample     = 1 * time.Minute
	maxResample     = 2 * time.Hour

	// Refilling the queue from the routing table, and widening it with
	// random-target find_node so we keep meeting nodes the table does not
	// already hold.
	refillInterval = 5 * time.Second
	walkInterval   = 2 * time.Second

	// Node-quality persistence (see nodequality.go): the best proven
	// nodes are snapshotted periodically and once at shutdown, and a
	// later start seeds both the quality tracker and the bootstrap set
	// from them, so discovery does not cold-start from the five public
	// routers alone.
	nodePersistInterval  = 5 * time.Minute
	nodePersistTop       = 512
	nodePersistMinScore  = 0.5
	nodePersistLoadLimit = 512
)

// Config controls the crawler.
type Config struct {
	// Port is the UDP listen port; 0 picks a random one. With more
	// than one identity, identity i listens on Port+i (Port=0 keeps
	// every identity on its own random port).
	Port int
	// Enabled=false disables the crawler entirely (offline environments).
	Enabled bool
	// Samplers is the number of concurrent BEP 51 sampling workers
	// per identity, so the total is Samplers × Identities.
	// 0 selects defaultSamplers.
	Samplers int
	// Identities is how many DHT node identities to run. Each is a
	// full DHT server with its own random node ID, UDP port, routing
	// table and node-quality tracker, all feeding the same harvest
	// channel. 0 or 1 selects a single identity — exactly the
	// historical behaviour; the value is clamped to [1, maxIdentities].
	Identities int
	// IPv6Enabled additionally runs an IPv6 DHT server (BEP 32) for
	// every identity, on the same port number as the identity's IPv4
	// socket (a separate udp6 socket, so the two never collide). The
	// IPv6 server shares the identity's node ID, quality tracker and
	// the shared harvest pipeline; only its socket, routing table and
	// sampler queues are its own, because a node address can only be
	// queried over a socket of its own address family. Strictly
	// best-effort: when the udp6 socket cannot be bound (a host
	// without IPv6, say) the crawler logs one line and runs IPv4-only
	// — an IPv6 failure is never fatal, unlike identity 0's IPv4
	// start.
	IPv6Enabled bool
	// Logger, nil for log.Default().
	Logger *log.Logger
	// LoadSeedNodes, when non-nil, supplies node-quality state saved
	// by a previous run. The restored nodes seed every identity's
	// quality tracker and join every identity's bootstrap set (the
	// public routers stay as fallback: if every restored node is dead
	// by now, bootstrap must still work). Nil disables node persistence.
	LoadSeedNodes func(ctx context.Context) ([]PersistedNode, error)
	// SaveSeedNodes, when non-nil, receives the merged top-quality
	// nodes of all identities every nodePersistInterval and once more
	// at Close. Nil disables node persistence.
	SaveSeedNodes func(ctx context.Context, nodes []PersistedNode) error
}

// clampIdentities normalises Config.Identities into [1, maxIdentities].
func clampIdentities(n int) int {
	if n < 1 {
		return 1
	}
	if n > maxIdentities {
		return maxIdentities
	}
	return n
}

// identity is one running DHT node identity: a server with its own
// node ID, socket, routing table, quality tracker and sampler pool.
// Everything that made a lone Crawler stateful lives here now; the
// Crawler owns only the shared harvest pipeline (dedup set, output
// channel, counters) that every identity feeds.
type identity struct {
	c   *Crawler // parent: shared push/counters/logger
	idx int

	server *dht.Server // the IPv4 endpoint's server (eps[0]); kept as the identity's face

	// eps holds the identity's server endpoints: eps[0] is IPv4 and
	// eps[1], when IPv6 is enabled and bound, is IPv6 (BEP 32). Both
	// run under the identity's one node ID and share its quality
	// tracker; the sampling machinery runs per endpoint (see
	// endpoint). Empty for a hand-built identity (unit tests), whose
	// own fields below then ARE the IPv4 endpoint.
	eps []*endpoint

	// Node quality scoring (nodequality.go), this identity's own:
	// scores are only ever compared within one table, because a node
	// that answers this identity proves nothing about its behaviour
	// toward another identity's neighbourhood.
	quality  *nodeQuality
	restored int // persisted nodes seeded into this identity

	// Sampling state of the IPv4 endpoint (eps[0]), aliased by
	// startIdentity. nodeQ carries nodes waiting to be sampled; nodeNext
	// records the earliest time each address is worth sampling again, which
	// keeps the samplers off nodes they just read. refill offers the table
	// best-first by quality score, so after a refill the queue drains
	// toward the nodes most likely to answer with fresh samples.
	nodeQ    chan krpc.NodeAddr
	nodeMu   *sync.Mutex
	nodeNext map[string]time.Time
}

// endpoint is one DHT server socket of an identity: the IPv4 one, or
// — when IPv6 is enabled and the socket binds — the IPv6 (BEP 32)
// one on the same port number. The sampling machinery (refill, walk,
// sample loops) runs per endpoint because a node address can only
// ever be queried over a socket of its own address family: a v6 node
// sent over the v4 socket is undeliverable, not merely slow. The
// endpoint owns its server, sampler queue and resample schedule; the
// identity's quality tracker (keyed by address string, so families
// never collide) and the crawler's harvest pipeline stay shared.
type endpoint struct {
	id     *identity
	server *dht.Server
	v6     bool

	nodeQ    chan krpc.NodeAddr
	nodeMu   *sync.Mutex // the identity's, shared with the sibling endpoint
	nodeNext map[string]time.Time
}

// endpointFor routes a node address to the endpoint that can query
// it: IPv4 addresses to the IPv4 endpoint, IPv6 addresses to the
// IPv6 one. Nil when the identity has no endpoint of that family —
// an IPv6 node with IPv6 disabled has nowhere to go and is dropped.
func (id *identity) endpointFor(na krpc.NodeAddr) *endpoint {
	v6 := na.IP.To4() == nil
	if len(id.eps) == 0 {
		// Hand-built identity (unit tests): its own fields are the
		// IPv4 endpoint, wrapped on the fly like selfIdentity does.
		if v6 {
			return nil
		}
		return &endpoint{id: id, server: id.server, nodeQ: id.nodeQ, nodeMu: id.nodeMu, nodeNext: id.nodeNext}
	}
	for _, ep := range id.eps {
		if ep.v6 == v6 {
			return ep
		}
	}
	return nil
}

// v6Endpoint returns the identity's IPv6 endpoint, or nil.
func (id *identity) v6Endpoint() *endpoint {
	for _, ep := range id.eps {
		if ep.v6 {
			return ep
		}
	}
	return nil
}

// Crawler is a running (or disabled) DHT crawler: one or more
// identities feeding one shared harvest pipeline.
type Crawler struct {
	enabled  bool
	samplers int
	out      chan string
	cancel   context.CancelFunc
	logger   *log.Logger

	// IPv6 (BEP 32): ipv6Enabled is Config.IPv6Enabled as started;
	// ipv6Down latches the first IPv6 endpoint failure so Start logs
	// the one fallback line and stops attempting further udp6 binds
	// (same host, same stack: a bind failure for one identity would
	// repeat identically for the rest).
	ipv6Enabled bool
	ipv6Down    bool

	identities []*identity

	// Identity 0's state, aliased from identities[0] by Start so the
	// fields keep meaning what they always have. A Crawler assembled
	// by hand (unit tests) has no identities slice, and then these
	// fields ARE identity 0 — selfIdentity wraps them on the fly.
	server   *dht.Server
	quality  *nodeQuality
	nodeQ    chan krpc.NodeAddr
	nodeMu   sync.Mutex
	nodeNext map[string]time.Time
	restored int // persisted nodes loaded at Start (the shared seed set)

	// Node-persistence hook (nodequality.go).
	saveNodes func(ctx context.Context, nodes []PersistedNode) error

	mu    sync.Mutex
	seen  map[[20]byte]struct{}
	ring  [][20]byte // FIFO eviction order for seen
	known [][20]byte // recent infohashes for active probing

	// Peer hints gathered from DHT traffic (peerhints.go), consumed
	// by the metadata queue bridge as items are built. Shared by all
	// identities: a peer for a hash is a peer regardless of which of
	// our node IDs learned it.
	hints peerHintCache

	// Counters, read via Stats. Shared by all identities: they count
	// harvest-pipeline events, not per-identity activity.
	statMu    sync.Mutex
	sampled   int64 // sample_infohashes queries answered
	sampleErr int64 // queries that failed or returned nothing
	harvested int64 // infohashes returned by samples (before dedup)
	seenV6    int64 // infohashes first discovered via an IPv6 source
}

// Stats reports sampler activity, aggregated over all identities.
// Nodes is the summed routing table size, which is the pool the
// samplers draw from — if it collapses, discovery stalls. The
// node-quality fields describe the scoring layer (nodequality.go),
// likewise summed: TrackedNodes counts tracker entries across
// identities (the same address tracked by two identities counts
// twice — the entries are independent scorings), AvgNodeScore is the
// mean score of nodes with at least one observation across all
// trackers, BannedNodes how many entries are parked in a failure
// cooldown, and RestoredNodes how many persisted nodes were loaded at
// startup. Identities is how many DHT identities are running. The
// IPv6 fields report the BEP 32 side: IPv6Enabled is what was
// configured, IPv6Bound how many identities actually got an IPv6
// server (0 on a host without IPv6 — the graceful fallback),
// IPv6Nodes the summed routing size of the IPv6 servers (already
// included in Nodes), and SeenV6 how many infohashes were first
// discovered through an IPv6 source (an IPv6 sample reply, an
// announce_peer or get_peers arriving over IPv6).
type Stats struct {
	Nodes         int
	Queued        int
	Sampled       int64
	SampleErr     int64
	Harvested     int64
	TrackedNodes  int
	AvgNodeScore  float64
	BannedNodes   int
	RestoredNodes int
	Identities    int
	IPv6Enabled   bool
	IPv6Bound     int
	IPv6Nodes     int
	SeenV6        int64
}

// Start launches the crawler. With Enabled=false it returns a Crawler whose
// Infohashes channel is nil; all other methods remain safe to call.
func Start(cfg Config) (*Crawler, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	samplers := cfg.Samplers
	if samplers <= 0 {
		samplers = defaultSamplers
	}
	nIdent := clampIdentities(cfg.Identities)
	c := &Crawler{
		enabled:     cfg.Enabled,
		samplers:    samplers,
		logger:      logger,
		seen:        make(map[[20]byte]struct{}, seenCapacity),
		saveNodes:   cfg.SaveSeedNodes,
		ipv6Enabled: cfg.IPv6Enabled,
	}
	if !cfg.Enabled {
		return c, nil
	}

	// Restore persisted node quality before any server exists: the
	// snapshot is loaded once and every identity seeds its tracker
	// from the same set, and the addresses join every identity's
	// bootstrap set. A load failure is not fatal — bootstrap falls
	// back to the public routers alone, exactly as before.
	var saved []PersistedNode
	if cfg.LoadSeedNodes != nil {
		lctx, lcancel := context.WithTimeout(context.Background(), 10*time.Second)
		rows, lerr := cfg.LoadSeedNodes(lctx)
		lcancel()
		if lerr != nil {
			logger.Printf("crawler: load persisted nodes: %v (continuing without)", lerr)
		} else {
			saved = rows
		}
	}

	c.out = make(chan string, channelSize)
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	// Identity 0 failing to start is fatal, as a lone crawler's start
	// failure has always been. An extra identity failing (its port is
	// taken, say) only narrows coverage, so it is logged loudly and
	// skipped — the identities that did start keep harvesting.
	for i := 0; i < nIdent; i++ {
		id, err := c.startIdentity(ctx, i, cfg.Port, saved)
		if err != nil {
			if i == 0 {
				cancel()
				return nil, err
			}
			logger.Printf("crawler: DHT identity %d of %d failed to start: %v (continuing with %d)",
				i+1, nIdent, err, len(c.identities))
			continue
		}
		c.identities = append(c.identities, id)
		if i == 0 && len(saved) > 0 {
			c.restored = id.restored
			logger.Printf("crawler: restored %d persisted nodes into the bootstrap set", c.restored)
		}
	}
	// Identity 0's state backs the Crawler-level fields.
	id0 := c.identities[0]
	c.server, c.quality, c.nodeQ, c.nodeNext = id0.server, id0.quality, id0.nodeQ, id0.nodeNext
	if c.saveNodes != nil {
		go c.persistLoop(ctx)
	}
	if nIdent > 1 {
		logger.Printf("crawler: %d DHT identities running, %d samplers each (%d total)",
			len(c.identities), c.samplers, len(c.identities)*c.samplers)
	}
	return c, nil
}

// bootstrapNodes6 are extra bootstrap hostnames for the IPv6
// servers only: the anacrolix library's own default bootstrap set
// (dht.DefaultGlobalBootstrapHostPorts) marks router.silotis.us as
// its IPv6 entry, while the shared bootstrapNodes above are the
// long-standing IPv4 routers. The IPv6 server resolves the shared
// list too and keeps whatever AAAA answers exist; this is the
// dedicated v6 seed on top.
var bootstrapNodes6 = []string{
	"router.silotis.us:6881",
}

// startIdentity brings up identity idx: its own UDP socket (base port
// + idx, or a random port when the base is 0), an explicit random
// node ID, and its own refill/walk/sampler loops, all feeding the
// Crawler's shared harvest pipeline. When IPv6 is enabled the
// identity additionally gets an IPv6 endpoint on the same port
// number; its failure is logged once (crawler-wide) and never
// propagates — only the IPv4 start can fail an identity.
func (c *Crawler) startIdentity(ctx context.Context, idx, basePort int, saved []PersistedNode) (*identity, error) {
	id := &identity{
		c:       c,
		idx:     idx,
		quality: newNodeQuality(),
		nodeMu:  new(sync.Mutex),
	}
	seedAddrs := id.quality.Seed(saved)
	id.restored = len(seedAddrs)

	port := basePort
	if port > 0 {
		port += idx
		if port > 65535 {
			return nil, fmt.Errorf("listen port %d out of range", port)
		}
	}
	// An explicit random node ID per identity, shared by its IPv4
	// and IPv6 servers — one identity is one node (BEP 32 dual-stack
	// nodes present the same ID on both networks). Left to the
	// library the ID would still be random today (PublicIP is
	// unset), but the identities must be distinct by construction,
	// not by accident of library defaults.
	nodeID := randomID()

	ep4, err := c.startEndpoint(ctx, id, nodeID, port, false, seedAddrs)
	if err != nil {
		return nil, err
	}
	id.eps = append(id.eps, ep4)
	id.server, id.nodeQ, id.nodeNext = ep4.server, ep4.nodeQ, ep4.nodeNext

	if c.ipv6Enabled && !c.ipv6Down {
		// Same port number as the IPv4 socket actually bound (they
		// differ when the base port was 0 and IPv4 picked a random
		// one). A separate udp6 socket, so no port collision.
		v6port := ep4.server.Addr().(*net.UDPAddr).Port
		ep6, err6 := c.startEndpoint(ctx, id, nodeID, v6port, true, seedAddrs)
		if err6 != nil {
			c.ipv6Down = true
			c.logger.Printf("dht: IPv6 unavailable (%v), continuing IPv4-only", err6)
		} else {
			id.eps = append(id.eps, ep6)
		}
	}

	// Offer the restored nodes to the samplers right away: they are
	// proven sample sources, and asking them immediately is what turns
	// the persisted set into first samples within seconds instead of
	// waiting for the routing table to fill around them. enqueueNode
	// routes each address to the endpoint of its own family, so the
	// IPv6 seeds land on the IPv6 endpoint when there is one (and are
	// dropped when there is not — the IPv4 socket cannot query them).
	for _, a := range seedAddrs {
		if ua, rerr := net.ResolveUDPAddr("udp", a); rerr == nil && ua.Port != 0 {
			id.enqueueNode(krpc.NodeAddr{IP: ua.IP, Port: ua.Port})
		}
	}
	return id, nil
}

// startEndpoint brings up one server socket of an identity: an
// explicit udp4 or udp6 listen on port, a dht.Server under the
// identity's node ID seeded from its own family's share of the
// restored nodes and bootstrap routers, and the refill/walk/sampler
// loops that feed the shared harvest pipeline from this socket.
func (c *Crawler) startEndpoint(ctx context.Context, id *identity, nodeID [20]byte, port int, v6 bool, seedAddrs []string) (*endpoint, error) {
	network := "udp4"
	if v6 {
		network = "udp6"
	}
	conn, err := net.ListenUDP(network, &net.UDPAddr{Port: port})
	if err != nil {
		return nil, err
	}
	ep := &endpoint{
		id:       id,
		v6:       v6,
		nodeQ:    make(chan krpc.NodeAddr, nodeQueueSize),
		nodeMu:   id.nodeMu,
		nodeNext: make(map[string]time.Time),
	}
	sc := dht.NewDefaultServerConfig()
	sc.Conn = conn
	sc.NodeId = krpc.ID(nodeID)
	sc.StartingNodes = func() ([]dht.Addr, error) {
		return startingNodes(seedAddrs, v6)
	}
	sc.OnQuery = c.onQuery
	sc.OnAnnouncePeer = c.onAnnouncePeer
	s, err := dht.NewServer(sc)
	if err != nil {
		conn.Close()
		return nil, err
	}
	ep.server = s

	// The library does not run this itself: NewServer's contract is that the
	// caller starts it once the server is set up. Without it the routing
	// table is whatever the initial bootstrap returned and slowly decays, so
	// the samplers run out of nodes to ask. It bootstraps on its own, so an
	// explicit Bootstrap alongside it only races and logs "already
	// bootstrapping".
	go s.TableMaintainer()
	go ep.refillLoop(ctx)
	go ep.walkLoop(ctx)
	for i := 0; i < c.samplers; i++ {
		go ep.sampleLoop(ctx)
	}
	c.logger.Printf("crawler: DHT node listening on %s (%d samplers)",
		conn.LocalAddr(), c.samplers)
	return ep, nil
}

// startingNodes builds one endpoint's bootstrap set: the restored
// nodes of its own address family plus the public routers that
// resolve into that family. The families are seeded separately
// because a server can only ever query addresses its own socket can
// reach — handing the IPv4 server IPv6 starting nodes (or the
// reverse) only buys failed sends. The IPv4 side keeps the
// historical error contract (resolution failure with nothing else
// is an error); the IPv6 side never errors — an empty set just
// means the v6 table fills from inbound traffic and nodes6 replies
// instead, and IPv6 must never take the crawler down.
func startingNodes(seedAddrs []string, v6 bool) ([]dht.Addr, error) {
	out := seedAddrsForFamily(seedAddrs, v6)
	hosts := bootstrapNodes
	if v6 {
		hosts = make([]string, 0, len(bootstrapNodes)+len(bootstrapNodes6))
		hosts = append(hosts, bootstrapNodes...)
		hosts = append(hosts, bootstrapNodes6...)
	}
	pub, rerr := dht.ResolveHostPorts(hosts)
	for _, a := range pub {
		if (a.IP().To4() == nil) == v6 {
			out = append(out, a)
		}
	}
	if rerr != nil && len(out) == 0 && !v6 {
		return nil, rerr
	}
	return out, nil
}

// seedAddrsForFamily resolves the persisted seed addresses down to
// one family's share, in dht.Addr form. Unresolvable or
// wrong-family entries are skipped: the snapshot is one merged set
// for both families (see mergedTopNodes), and each endpoint picks
// out the addresses it can actually use.
func seedAddrsForFamily(seedAddrs []string, v6 bool) []dht.Addr {
	network := "udp4"
	if v6 {
		network = "udp6"
	}
	var out []dht.Addr
	for _, a := range seedAddrs {
		ua, err := net.ResolveUDPAddr(network, a)
		// ua.IP is nil for a hostless string ("", ":6881"): not a
		// node address at all, whatever family it claims to be.
		if err != nil || ua.IP == nil || ua.Port == 0 || (ua.IP.To4() == nil) != v6 {
			continue
		}
		out = append(out, dht.NewAddr(ua))
	}
	return out
}

// onAnnouncePeer observes an inbound announce_peer, identically for
// both endpoint families: the hook receives the announcer's own
// address, whose family says which network the discovery came from.
func (c *Crawler) onAnnouncePeer(infoHash metainfo.Hash, ip net.IP, port int, portOk bool) {
	ih := [20]byte(infoHash)
	// The announcer is a peer for this hash by definition, and
	// its address arrives with the query — record it as a fetch
	// hint BEFORE pushing the hash, so the hint is already in
	// the cache when the queue bridge picks the hash up. portOk
	// is false when neither an announced port nor an implied
	// source port could be derived; a hint without a port is
	// worthless, so those announces contribute the hash only.
	if portOk && port > 0 && port <= 65535 {
		if ap, ok := addrPortFromIP(ip, uint16(port)); ok {
			c.recordPeerHint(ih, ap)
		}
	}
	c.pushFrom(ih, ip != nil && ip.To4() == nil)
}

// selfIdentity returns identity 0: the first started identity, or —
// for a Crawler assembled by hand rather than via Start — a wrapper
// over the Crawler's own fields, which are identity 0 in that case.
func (c *Crawler) selfIdentity() *identity {
	if len(c.identities) > 0 {
		return c.identities[0]
	}
	return &identity{
		c: c, server: c.server, quality: c.quality,
		nodeQ: c.nodeQ, nodeMu: &c.nodeMu, nodeNext: c.nodeNext,
		restored: c.restored,
	}
}

// enqueueNode and scheduleResample stay on Crawler as identity-0
// shorthands; the sampling machinery itself lives on identity.
func (c *Crawler) enqueueNode(na krpc.NodeAddr) { c.selfIdentity().enqueueNode(na) }

func (c *Crawler) scheduleResample(na krpc.NodeAddr, interval *int64) {
	c.selfIdentity().scheduleResample(na, interval)
}

// persistLoop snapshots the top-quality nodes for the next start.
func (c *Crawler) persistLoop(ctx context.Context) {
	t := time.NewTicker(nodePersistInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.saveQuality()
		}
	}
}

// saveQuality writes the current persistence snapshot through the
// configured hook. Safe to call when persistence is disabled. The
// enabled check matters: a crawler started disabled holds an empty
// tracker, and saving it would wipe the previous run's snapshot.
func (c *Crawler) saveQuality() {
	if !c.enabled || c.saveNodes == nil {
		return
	}
	nodes := c.mergedTopNodes()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.saveNodes(ctx, nodes); err != nil {
		c.logger.Printf("crawler: persist nodes: %v", err)
	}
}

// mergedTopNodes builds the single persistence snapshot from all
// identities. The dht_nodes table holds one replace-all snapshot, so
// the identities must not take turns overwriting it with their own
// top sets; instead each contributes its Top set and the results are
// merged here: an address proven to several identities collapses to
// the entry with the most observations behind it, and the merged set
// is capped at nodePersistTop so the stored snapshot stays the size
// it has always been. The snapshot is deliberately shared rather than
// keyed per identity: the addresses are global DHT nodes, every
// identity seeds from the same merged set at the next start and
// re-scores them independently from there, and keying the table per
// identity would store N largely identical copies of the proven set
// for no bootstrap benefit.
func (c *Crawler) mergedTopNodes() []PersistedNode {
	if len(c.identities) == 0 {
		return c.quality.Top(nodePersistTop, nodePersistMinScore)
	}
	best := make(map[string]PersistedNode)
	for _, id := range c.identities {
		for _, n := range id.quality.Top(nodePersistTop, nodePersistMinScore) {
			if cur, ok := best[n.Addr]; !ok || n.Attempts > cur.Attempts {
				best[n.Addr] = n
			}
		}
	}
	out := make([]PersistedNode, 0, len(best))
	for _, n := range best {
		out = append(out, n)
	}
	// Best first, following nodeQuality.Top's ordering discipline:
	// score, then the evidence behind it.
	sort.Slice(out, func(i, j int) bool {
		si := out[i].SuccW / (out[i].SuccW + out[i].FailW)
		sj := out[j].SuccW / (out[j].SuccW + out[j].FailW)
		if si != sj {
			return si > sj
		}
		return out[i].Attempts > out[j].Attempts
	})
	if len(out) > nodePersistTop {
		out = out[:nodePersistTop]
	}
	return out
}

// Infohashes returns the channel of newly seen infohashes (hex encoded).
// It is nil when the crawler is disabled.
func (c *Crawler) Infohashes() <-chan string { return c.out }

// Enabled reports whether the crawler is running.
func (c *Crawler) Enabled() bool { return c.enabled }

// SeenCount returns how many unique infohashes are currently remembered.
func (c *Crawler) SeenCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}

// Stats returns sampler counters. Safe on a disabled crawler.
func (c *Crawler) Stats() Stats {
	c.statMu.Lock()
	st := Stats{
		Sampled:   c.sampled,
		SampleErr: c.sampleErr,
		Harvested: c.harvested,
		SeenV6:    c.seenV6,
	}
	c.statMu.Unlock()
	st.RestoredNodes = c.restored
	st.IPv6Enabled = c.ipv6Enabled
	if len(c.identities) > 0 {
		st.Identities = len(c.identities)
		var scored int
		var scoreSum float64
		for _, id := range c.identities {
			for _, ep := range id.eps {
				if ep.server == nil {
					continue
				}
				n := ep.server.NumNodes()
				st.Nodes += n
				st.Queued += len(ep.nodeQ)
				if ep.v6 {
					st.IPv6Nodes += n
					st.IPv6Bound++
				}
			}
			tracked, sc, ss, banned := id.quality.aggregate()
			st.TrackedNodes += tracked
			st.BannedNodes += banned
			scored += sc
			scoreSum += ss
		}
		if scored > 0 {
			st.AvgNodeScore = scoreSum / float64(scored)
		}
		return st
	}
	// No started identities: a disabled crawler (everything zero) or
	// a hand-built one whose fields are identity 0.
	if c.server != nil {
		st.Identities = 1
		st.Nodes = c.server.NumNodes()
	}
	st.Queued = len(c.nodeQ)
	if c.quality != nil {
		qs := c.quality.Summary()
		st.TrackedNodes = qs.Tracked
		st.AvgNodeScore = qs.AvgScore
		st.BannedNodes = qs.Banned
	}
	return st
}

// Close shuts the crawler down.
func (c *Crawler) Close() {
	// Final quality snapshot while the store is still open (the
	// caller closes it after us); saveQuality no-ops without a hook.
	c.saveQuality()
	if c.cancel != nil {
		c.cancel()
	}
	if len(c.identities) > 0 {
		for _, id := range c.identities {
			for _, ep := range id.eps {
				if ep.server != nil {
					ep.server.Close()
				}
			}
		}
	} else if c.server != nil {
		c.server.Close()
	}
	if c.out != nil {
		close(c.out)
	}
}

// onQuery observes inbound queries. It always returns true so the default
// handlers reply (nodes/tokens/samples) — the node stays a good DHT citizen.
func (c *Crawler) onQuery(query *krpc.Msg, source net.Addr) bool {
	if query.A == nil {
		return true
	}
	switch query.Q {
	case "get_peers":
		c.pushFrom(query.A.InfoHash, isV6Source(source))
	}
	return true
}

// isV6Source reports whether a packet source address is IPv6. The
// library hands OnQuery the raw source address; anything that is
// not recognisably IPv6 counts as IPv4 for attribution.
func isV6Source(a net.Addr) bool {
	if ua, ok := a.(*net.UDPAddr); ok {
		return ua.IP.To4() == nil
	}
	return false
}

// push records an infohash if new, with FIFO eviction over the capacity.
func (c *Crawler) push(ih [20]byte) { c.pushFrom(ih, false) }

// pushFrom is push with discovery-source attribution: v6 marks the
// infohash as first seen through an IPv6 source (an IPv6 sample
// reply, or an announce_peer / get_peers arriving over IPv6), which
// Stats reports as SeenV6. Only the first sighting counts — a hash
// already known from IPv4 is not re-attributed when an IPv6 source
// repeats it.
func (c *Crawler) pushFrom(ih [20]byte, v6 bool) {
	// The all-zero infohash is not a torrent: peers send it in malformed or
	// probing queries, and BEP 51 samples occasionally carry it. It has to be
	// dropped here because anacrolix/torrent panics rather than errors when
	// AddMagnet is handed one, which takes the whole process down.
	if ih == ([20]byte{}) {
		return
	}
	c.mu.Lock()
	if _, ok := c.seen[ih]; ok {
		c.mu.Unlock()
		return
	}
	if v6 {
		c.addStat(&c.seenV6, 1)
	}
	if len(c.ring) >= seenCapacity {
		delete(c.seen, c.ring[0])
		c.ring = c.ring[1:]
	}
	c.seen[ih] = struct{}{}
	c.ring = append(c.ring, ih)
	if len(c.known) >= knownCapacity {
		c.known = c.known[1:]
	}
	c.known = append(c.known, ih)
	out := c.out
	c.mu.Unlock()

	if out != nil {
		select {
		case out <- hex.EncodeToString(ih[:]):
		default: // downstream busy; drop rather than block the DHT handler
		}
	}
}

// refillLoop keeps the sampler queue fed from the routing table. Nodes that
// were sampled recently are skipped by enqueue, so this re-offers the whole
// table cheaply and only the due ones get through. The table holds
// both families (replies carry nodes and nodes6 alike), so offers go
// through the identity's family routing: a node is always sampled
// over the endpoint whose socket can reach it.
func (ep *endpoint) refillLoop(ctx context.Context) {
	id := ep.id
	t := time.NewTicker(refillInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		nodes := ep.server.Nodes()
		sort.Slice(nodes, func(i, j int) bool {
			return id.quality.Score(nodes[i].Addr.String()) > id.quality.Score(nodes[j].Addr.String())
		})
		for _, n := range nodes {
			id.enqueueNode(n.Addr)
		}
	}
}

// walkLoop widens coverage beyond the routing table. find_node against a
// random target returns the nodes closest to it, which is how we reach parts
// of the ID space our own table does not cover — and each new node is a new
// sample set. Without this the samplers keep re-reading the same neighbours.
// The walk target is picked from this endpoint's own family: the table
// mixes families, and querying a node over the wrong family's socket
// can only fail.
func (ep *endpoint) walkLoop(ctx context.Context) {
	id := ep.id
	t := time.NewTicker(walkInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		all := ep.server.Nodes()
		nodes := all[:0]
		for _, n := range all {
			if (n.Addr.IP.To4() == nil) == ep.v6 {
				nodes = append(nodes, n)
			}
		}
		if len(nodes) == 0 {
			continue
		}
		node := nodes[randIntn(len(nodes))]
		start := time.Now()
		res := ep.server.FindNode(
			dht.NewAddr(node.Addr.UDP()), int160.FromByteArray(randomID()),
			dht.QueryRateLimiting{},
		)
		// find_node is the walk's own query/response exchange with this
		// node: fold the outcome into its quality score.
		if res.Err == nil && res.Reply.R != nil {
			id.quality.Record(node.Addr.String(), true, time.Since(start))
		} else {
			id.quality.Record(node.Addr.String(), false, 0)
		}
		ep.absorbNodes(res)

		// Piggyback a get_peers for something recently seen: it keeps this
		// node in other peers' routing tables, which is what makes inbound
		// get_peers traffic (the passive half of discovery) arrive at all.
		// The reply's values are peers for that hash: harvest them as
		// fetch hints (peerhints.go) instead of discarding the result,
		// which is what happened to it before.
		if ih, ok := id.c.randomKnown(); ok {
			res := ep.server.GetPeers(ctx, dht.NewAddr(node.Addr.UDP()),
				int160.FromByteArray(ih), false, dht.QueryRateLimiting{})
			if res.Reply.R != nil {
				for _, va := range res.Reply.R.Values {
					if ap, ok := addrPortFromNodeAddr(va); ok {
						id.c.recordPeerHint(ih, ap)
					}
				}
			}
		}
	}
}

// sampleLoop pulls nodes off the queue and asks each for a sample of the
// infohashes it knows (BEP 51). This is the active half of discovery and the
// one that scales: every distinct node answers with up to a few dozen
// infohashes we would otherwise have to wait for peers to announce.
func (ep *endpoint) sampleLoop(ctx context.Context) {
	for {
		var addr krpc.NodeAddr
		select {
		case <-ctx.Done():
			return
		case addr = <-ep.nodeQ:
		}
		ep.sampleNode(ctx, addr)
	}
}

func (ep *endpoint) sampleNode(ctx context.Context, na krpc.NodeAddr) {
	id := ep.id
	qctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()

	// The target is required by BEP 51 and is not cosmetic: it selects which
	// part of the ID space the node reports on, and its reply carries the
	// nodes closest to that target. Randomising it per query is what turns
	// repeated sampling into a walk instead of a re-read.
	start := time.Now()
	res := ep.server.Query(qctx, dht.NewAddr(na.UDP()), "sample_infohashes", dht.QueryInput{
		MsgArgs:      krpc.MsgArgs{ID: ep.server.ID(), Target: randomID()},
		RateLimiting: dht.QueryRateLimiting{},
	})
	ep.absorbNodes(res)

	if res.Err != nil || res.Reply.R == nil || res.Reply.R.Samples == nil {
		id.quality.Record(na.String(), false, 0)
		id.c.bump(&id.c.sampleErr)
		return
	}
	id.quality.Record(na.String(), true, time.Since(start))
	samples := *res.Reply.R.Samples
	id.c.bump(&id.c.sampled)
	id.c.addStat(&id.c.harvested, int64(len(samples)))
	for _, ih := range samples {
		id.c.pushFrom(ih, ep.v6)
	}
	ep.scheduleResample(na, res.Reply.R.Interval)
}

// scheduleResample honours the node's advertised refresh interval, scaled
// by its quality score — proven nodes are re-read sooner (factor 0.5 at
// score 1), poor ones later (factor 1.5 at score 0) — then clamped so a
// hostile or broken value cannot park a node forever or turn it into a
// hot loop.
func (ep *endpoint) scheduleResample(na krpc.NodeAddr, interval *int64) {
	d := defaultResample
	if interval != nil && *interval > 0 {
		d = time.Duration(*interval) * time.Second
	}
	d = time.Duration(float64(d) * (1.5 - ep.id.quality.Score(na.String())))
	d = min(max(d, minResample), maxResample)
	ep.nodeMu.Lock()
	ep.nodeNext[na.String()] = time.Now().Add(d)
	ep.nodeMu.Unlock()
}

// scheduleResample on the identity routes to the endpoint of the
// address's family, like enqueueNode.
func (id *identity) scheduleResample(na krpc.NodeAddr, interval *int64) {
	if ep := id.endpointFor(na); ep != nil {
		ep.scheduleResample(na, interval)
	}
}

// absorbNodes feeds the nodes carried by a query reply back into the
// sampler queues. Replies carry both families (nodes and nodes6);
// the identity's enqueue routes each address to its own family's
// endpoint, so an IPv6 node learned over IPv4 is sampled by the IPv6
// endpoint — and dropped when there is none.
func (ep *endpoint) absorbNodes(res dht.QueryResult) {
	if res.Reply.R == nil {
		return
	}
	for _, n := range res.Reply.R.Nodes {
		ep.id.enqueueNode(n.Addr)
	}
	for _, n := range res.Reply.R.Nodes6 {
		ep.id.enqueueNode(n.Addr)
	}
}

// enqueueNode on the identity routes a node address to the endpoint
// of its family (see endpointFor): a node can only be sampled over a
// socket of its own address family.
func (id *identity) enqueueNode(na krpc.NodeAddr) {
	if ep := id.endpointFor(na); ep != nil {
		ep.enqueueNode(na)
	}
}

// enqueueNode offers a node to the samplers, dropping it when it is not due
// for a resample or the queue is full. Dropping is correct here: the routing
// table refill re-offers everything a few seconds later, and blocking would
// stall whichever query reply we are walking.
func (ep *endpoint) enqueueNode(na krpc.NodeAddr) {
	id := ep.id
	if na.Port == 0 {
		return
	}
	key := na.String()
	// Register the node in the quality tracker and respect a failure
	// cooldown: a persistently dead node stops consuming sampler slots
	// until its ban expires (see nodeQuality.Banned).
	id.quality.Touch(key)
	if id.quality.Banned(key) {
		return
	}
	now := time.Now()

	ep.nodeMu.Lock()
	if next, ok := ep.nodeNext[key]; ok && now.Before(next) {
		ep.nodeMu.Unlock()
		return
	}
	if len(ep.nodeNext) >= nodeTrackMax {
		// Cheap bound: forget everything rather than track insertion order.
		// Re-learning a node costs one extra query, so the reset is harmless
		// and keeps the map from growing without limit.
		ep.nodeNext = make(map[string]time.Time, nodeTrackMax/2)
	}
	// Reserve the node now so the other samplers skip it while this query is
	// in flight; a successful sample overwrites this with the real interval.
	ep.nodeNext[key] = now.Add(minResample)
	ep.nodeMu.Unlock()

	select {
	case ep.nodeQ <- na:
	default:
	}
}

func randomID() [20]byte {
	var id [20]byte
	rand.Read(id[:])
	return id
}

func (c *Crawler) bump(p *int64) { c.addStat(p, 1) }

func (c *Crawler) addStat(p *int64, n int64) {
	c.statMu.Lock()
	*p += n
	c.statMu.Unlock()
}

func (c *Crawler) randomKnown() ([20]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.known) == 0 {
		return [20]byte{}, false
	}
	return c.known[randIntn(len(c.known))], true
}

func randIntn(n int) int {
	var b [8]byte
	rand.Read(b[:])
	return int(uint64(b[0])|uint64(b[1])<<8|uint64(b[2])<<16|uint64(b[3])<<24) % n
}
