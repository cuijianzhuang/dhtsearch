package crawler

import (
	"encoding/hex"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
)

// Peer hints: peer addresses the DHT side encounters for an infohash,
// kept briefly so the metadata fetcher can be handed them as a warm
// start instead of relying on tracker announces alone.
//
// Two sources feed the cache, both of them traffic the crawler
// already has — this layer adds no new DHT queries:
//
//   - inbound announce_peer queries: the announcer's own address is
//     a peer for the hash it announces, by definition of the query;
//   - replies to the walk loop's piggyback get_peers (walkLoop has
//     always issued one per tick to stay visible in other nodes'
//     tables): the reply's values are peers for that hash, and were
//     previously read by nobody.
//
// The fetcher's clients run with NoDHT and their periodic announcer
// off (see metadataClientConfig), so a swarm listed on none of the
// configured trackers otherwise has no peer source at all. A hint
// does not have to be good to be worth trying: a dead address costs
// one failed dial inside the fetch's existing budget, and prescreen
// still requires an actual connection before the fetch rides out
// its full timeout.
//
// Sizing. Discovery runs at ~40k hashes per 10 minutes, but hints
// are only recorded for the two sources above — announces are a
// small passive fraction of that stream, and the get_peers source
// is one query per walk tick — and entries are consumed (deleted)
// when the queue bridge attaches them to an item. The hard caps
// below bound the worst case regardless: hintCacheEntries entries
// of at most hintPeersPerHash addresses ≈ 8192 × 8 × 24 B of
// payload, a few MB with map overhead, and a 30-minute TTL keeps a
// hint from outliving the swarm state it described.
const (
	hintPeersPerHash = 8
	hintCacheEntries = 8192
	hintTTL          = 30 * time.Minute
)

// hintEntry is one hash's hint set. seq orders the entry in the
// cache's eviction ring; an entry re-added after expiry gets a new
// seq, so a stale ring slot can never evict its live replacement.
type hintEntry struct {
	peers []netip.AddrPort
	exp   time.Time
	seq   uint64
}

// hintRingSlot is one eviction-ring position. The seq pairs with
// hintEntry.seq: a slot left behind by a consumed or replaced entry
// no longer matches and is skipped, never evicting the live entry
// that reused the hash.
type hintRingSlot struct {
	ih  [20]byte
	seq uint64
}

// peerHintCache is a bounded, TTL'd infohash → peer-addresses map.
// The zero value is ready to use; the limit fields exist so tests
// can shrink them.
type peerHintCache struct {
	mu         sync.Mutex
	m          map[[20]byte]*hintEntry
	ring       []hintRingSlot // insertion order, may hold stale slots
	seq        uint64
	ttl        time.Duration
	maxEntries int
	maxPeers   int
	now        func() time.Time
}

func (c *peerHintCache) initLocked() {
	if c.m != nil {
		return
	}
	c.m = make(map[[20]byte]*hintEntry)
	if c.ttl <= 0 {
		c.ttl = hintTTL
	}
	if c.maxEntries <= 0 {
		c.maxEntries = hintCacheEntries
	}
	if c.maxPeers <= 0 {
		c.maxPeers = hintPeersPerHash
	}
	if c.now == nil {
		c.now = time.Now
	}
}

// add records one peer address for a hash, deduplicated and capped
// per hash. Unusable addresses (no port, unspecified IP) are dropped
// here so consumers never have to re-check them.
func (c *peerHintCache) add(ih [20]byte, ap netip.AddrPort) {
	// Unmap before the unspecified check: the 4-in-6 form of
	// 0.0.0.0 does not report IsUnspecified until it is unmapped.
	ap = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	if !ap.IsValid() || ap.Port() == 0 || ap.Addr().IsUnspecified() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initLocked()
	now := c.now()
	if e, ok := c.m[ih]; ok && now.Before(e.exp) {
		for _, p := range e.peers {
			if p == ap {
				e.exp = now.Add(c.ttl)
				return
			}
		}
		if len(e.peers) < c.maxPeers {
			e.peers = append(e.peers, ap)
		}
		e.exp = now.Add(c.ttl)
		return
	}
	// New (or expired) entry. Make room first: evict oldest
	// insertions until under the cap. Ring slots whose entry was
	// consumed or replaced carry a stale seq and are just skipped.
	for len(c.m) >= c.maxEntries && len(c.ring) > 0 {
		slot := c.ring[0]
		c.ring = c.ring[1:]
		if e, ok := c.m[slot.ih]; ok && e.seq == slot.seq {
			delete(c.m, slot.ih)
		}
	}
	c.seq++
	c.m[ih] = &hintEntry{peers: []netip.AddrPort{ap}, exp: now.Add(c.ttl), seq: c.seq}
	c.ring = append(c.ring, hintRingSlot{ih: ih, seq: c.seq})
	// Bound the ring itself. Slots are consumed only by the
	// eviction loop above, which runs only when the map is at its
	// cap — but in the normal regime entries are taken (consumed)
	// or expire long before the cap is reached, so without this
	// the ring grew by one slot (~28 B) per distinct hinted hash
	// forever: a slow, unbounded leak riding under a bounded map.
	// When the ring exceeds twice the entry cap, rebuild it from
	// the live entries (in insertion order, so eviction order is
	// preserved). The rebuild is O(ring) but happens at most once
	// per maxEntries inserts, so inserts stay amortized O(1) and
	// len(ring) never exceeds 2*maxEntries+1.
	if len(c.ring) > 2*c.maxEntries {
		live := c.ring[:0]
		for _, slot := range c.ring {
			if e, ok := c.m[slot.ih]; ok && e.seq == slot.seq {
				live = append(live, slot)
			}
		}
		// Zero the tail so dropped slots' hashes don't linger in
		// the backing array beyond the live length.
		for i := len(live); i < len(c.ring); i++ {
			c.ring[i] = hintRingSlot{}
		}
		c.ring = live
	}
}

// take returns and deletes a hash's hints — a hint is spent on the
// one fetch it can warm-start, and keeping it for a later duplicate
// item would only re-dial addresses that already had their chance.
// Expired entries count as absent.
func (c *peerHintCache) take(ih [20]byte) []netip.AddrPort {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initLocked()
	e, ok := c.m[ih]
	if !ok {
		return nil
	}
	delete(c.m, ih)
	if !c.now().Before(e.exp) {
		return nil
	}
	return append([]netip.AddrPort(nil), e.peers...)
}

// len reports the live entry count (tests and diagnostics).
func (c *peerHintCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initLocked()
	return len(c.m)
}

// recordPeerHint stores one hint from the crawler's own DHT traffic
// (announce_peer hook, get_peers replies). Internal: the sources
// already hold the binary hash.
func (c *Crawler) recordPeerHint(ih [20]byte, ap netip.AddrPort) {
	c.hints.add(ih, ap)
}

// AddPeerHint records peer addresses learned out of band for a hex
// infohash. The crawler's own sources use the internal path; this is
// the seam for anything outside the package (and for tests).
func (c *Crawler) AddPeerHint(hash string, ap netip.AddrPort) {
	if ih, ok := decodeInfohash(hash); ok {
		c.hints.add(ih, ap)
	}
}

// TakePeerHints returns the peer hints held for a hex infohash and
// removes them: the queue bridge calls this as an item is built, so
// the hints ride that one item and no other. Nil when there is
// nothing (fresh, consumed, or expired).
func (c *Crawler) TakePeerHints(hash string) []netip.AddrPort {
	ih, ok := decodeInfohash(hash)
	if !ok {
		return nil
	}
	return c.hints.take(ih)
}

func decodeInfohash(hash string) ([20]byte, bool) {
	var ih [20]byte
	b, err := hex.DecodeString(hash)
	if err != nil || len(b) != len(ih) {
		return ih, false
	}
	copy(ih[:], b)
	return ih, true
}

// addrPortFromIP converts a packet-source IP and port to a hint
// address. The IP arrives as whatever the UDP stack reported
// (usually 16-byte for v4); Unmap canonicalises it so the fetcher's
// connected-peer comparison sees one form.
func addrPortFromIP(ip net.IP, port uint16) (netip.AddrPort, bool) {
	if port == 0 {
		return netip.AddrPort{}, false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.AddrPort{}, false
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(addr, port), true
}

// addrPortFromNodeAddr converts a get_peers reply value (already a
// decoded krpc.NodeAddr) to a hint address.
func addrPortFromNodeAddr(na krpc.NodeAddr) (netip.AddrPort, bool) {
	if na.Port <= 0 || na.Port > 65535 {
		return netip.AddrPort{}, false
	}
	if ip4 := na.IP.To4(); ip4 != nil {
		return netip.AddrPortFrom(netip.AddrFrom4(*(*[4]byte)(ip4)), uint16(na.Port)), true
	}
	if ip16 := na.IP.To16(); ip16 != nil {
		addr := netip.AddrFrom16(*(*[16]byte)(ip16)).Unmap()
		if addr.IsUnspecified() {
			return netip.AddrPort{}, false
		}
		return netip.AddrPortFrom(addr, uint16(na.Port)), true
	}
	return netip.AddrPort{}, false
}
