package crawler

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
)

func hintHash(b byte) [20]byte {
	var ih [20]byte
	ih[0] = b
	ih[19] = 0xAB
	return ih
}

func hintAddr(port uint16) netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, 0, 1}), port)
}

func TestPeerHintCacheAddTakeConsume(t *testing.T) {
	c := &peerHintCache{}
	ih := hintHash(1)
	c.add(ih, hintAddr(6881))
	c.add(ih, hintAddr(6882))

	got := c.take(ih)
	if len(got) != 2 || got[0] != hintAddr(6881) || got[1] != hintAddr(6882) {
		t.Fatalf("take = %v, want both hints in order", got)
	}
	// Consumed on use: a second take finds nothing.
	if got := c.take(ih); got != nil {
		t.Fatalf("second take = %v, want nil (hints are spent on one item)", got)
	}
	if got := c.take(hintHash(99)); got != nil {
		t.Fatalf("take of unknown hash = %v, want nil", got)
	}
}

func TestPeerHintCacheDedupAndPerHashCap(t *testing.T) {
	c := &peerHintCache{}
	ih := hintHash(2)
	for i := 0; i < 3; i++ {
		c.add(ih, hintAddr(7000)) // duplicate
	}
	for p := uint16(7001); p <= 7010; p++ {
		c.add(ih, hintAddr(p))
	}
	got := c.take(ih)
	if len(got) != hintPeersPerHash {
		t.Fatalf("take returned %d hints, want the per-hash cap %d", len(got), hintPeersPerHash)
	}
	seen := map[netip.AddrPort]bool{}
	for _, ap := range got {
		if seen[ap] {
			t.Fatalf("duplicate hint %v in %v", ap, got)
		}
		seen[ap] = true
	}
	if !seen[hintAddr(7000)] {
		t.Fatalf("first-added hint missing from %v", got)
	}
}

func TestPeerHintCacheTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := &peerHintCache{ttl: time.Minute, now: func() time.Time { return now }}
	ih := hintHash(3)
	c.add(ih, hintAddr(6881))

	now = now.Add(59 * time.Second)
	if got := c.take(ih); len(got) != 1 {
		t.Fatalf("take before expiry = %v, want the hint", got)
	}

	// A fresh add refreshes the expiry; crossing the refreshed
	// deadline without one makes the entry count as absent.
	c.add(ih, hintAddr(6881))
	now = now.Add(2 * time.Minute)
	if got := c.take(ih); got != nil {
		t.Fatalf("take after expiry = %v, want nil", got)
	}
	if c.len() != 0 {
		t.Fatalf("expired take left %d entries, want 0", c.len())
	}
}

func TestPeerHintCacheEvictsOldestAtCap(t *testing.T) {
	c := &peerHintCache{maxEntries: 4}
	for i := byte(0); i < 6; i++ {
		c.add(hintHash(i), hintAddr(6881))
	}
	if c.len() != 4 {
		t.Fatalf("cache holds %d entries, want the cap 4", c.len())
	}
	for i := byte(0); i < 2; i++ {
		if got := c.take(hintHash(i)); got != nil {
			t.Fatalf("oldest entry %d survived eviction: %v", i, got)
		}
	}
	for i := byte(2); i < 6; i++ {
		if got := c.take(hintHash(i)); len(got) != 1 {
			t.Fatalf("entry %d missing after eviction: %v", i, got)
		}
	}
}

// A consumed entry's ring slot must not evict the live entry that
// later reused the same hash (the seq guard).
func TestPeerHintCacheStaleRingSlot(t *testing.T) {
	c := &peerHintCache{maxEntries: 2}
	a, b, d := hintHash(10), hintHash(11), hintHash(12)
	c.add(a, hintAddr(1))
	c.add(b, hintAddr(2))
	c.take(a)             // consume a; its ring slot is now stale
	c.add(a, hintAddr(3)) // a re-enters with a new seq
	c.add(d, hintAddr(4)) // forces one eviction: b is the oldest live entry
	if got := c.take(a); len(got) != 1 || got[0] != hintAddr(3) {
		t.Fatalf("re-added entry was evicted by its own stale slot: %v", got)
	}
	if got := c.take(b); got != nil {
		t.Fatalf("b should have been the eviction victim, got %v", got)
	}
}

func TestPeerHintCacheRejectsUnusableAddrs(t *testing.T) {
	c := &peerHintCache{}
	ih := hintHash(4)
	c.add(ih, netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, 0, 1}), 0)) // no port
	c.add(ih, netip.AddrPortFrom(netip.IPv6Unspecified(), 6881))            // unspecified
	c.add(ih, netip.AddrPort{})                                             // invalid
	if got := c.take(ih); got != nil {
		t.Fatalf("unusable addresses were cached: %v", got)
	}
	if c.len() != 0 {
		t.Fatalf("cache holds %d entries after rejected adds, want 0", c.len())
	}
}

func TestCrawlerPeerHintRoundTrip(t *testing.T) {
	c := testCrawler()
	hash := "97a29535b9e2f0e46a1b09a4b0c0e4f0a1b2c3d4"
	ap := netip.MustParseAddrPort("203.0.113.7:51413")

	if got := c.TakePeerHints(hash); got != nil {
		t.Fatalf("take before add = %v, want nil", got)
	}
	c.AddPeerHint(hash, ap)
	got := c.TakePeerHints(hash)
	if len(got) != 1 || got[0] != ap {
		t.Fatalf("take = %v, want [%v]", got, ap)
	}
	if got := c.TakePeerHints(hash); got != nil {
		t.Fatalf("second take = %v, want nil (consumed)", got)
	}
	// Malformed hashes are ignored on both sides.
	c.AddPeerHint("not-hex", ap)
	if got := c.TakePeerHints("not-hex"); got != nil {
		t.Fatalf("take of malformed hash = %v, want nil", got)
	}
	if got := c.TakePeerHints("abcd"); got != nil {
		t.Fatalf("take of short hash = %v, want nil", got)
	}
}

func TestAddrPortFromIP(t *testing.T) {
	ap, ok := addrPortFromIP(net.ParseIP("192.0.2.1"), 6881)
	if !ok || ap.String() != "192.0.2.1:6881" {
		t.Fatalf("v4 = %v, %v", ap, ok)
	}
	// Packet sources usually arrive as 16-byte v4-in-v6; the hint
	// must come out in the plain v4 form the fetcher compares with.
	ap, ok = addrPortFromIP(net.ParseIP("192.0.2.1").To16(), 6881)
	if !ok || ap.String() != "192.0.2.1:6881" {
		t.Fatalf("v4-in-v6 = %v, %v", ap, ok)
	}
	ap, ok = addrPortFromIP(net.ParseIP("2001:db8::1"), 6881)
	if !ok || ap.String() != "[2001:db8::1]:6881" {
		t.Fatalf("v6 = %v, %v", ap, ok)
	}
	if _, ok := addrPortFromIP(net.ParseIP("192.0.2.1"), 0); ok {
		t.Fatal("port 0 accepted")
	}
	if _, ok := addrPortFromIP(net.IPv4zero, 6881); ok {
		t.Fatal("unspecified IP accepted")
	}
	if _, ok := addrPortFromIP(nil, 6881); ok {
		t.Fatal("nil IP accepted")
	}
}

func TestAddrPortFromNodeAddr(t *testing.T) {
	ap, ok := addrPortFromNodeAddr(krpc.NodeAddr{IP: net.IPv4(198, 51, 100, 2), Port: 51413})
	if !ok || ap.String() != "198.51.100.2:51413" {
		t.Fatalf("v4 = %v, %v", ap, ok)
	}
	ap, ok = addrPortFromNodeAddr(krpc.NodeAddr{IP: net.ParseIP("2001:db8::2"), Port: 9})
	if !ok || ap.String() != "[2001:db8::2]:9" {
		t.Fatalf("v6 = %v, %v", ap, ok)
	}
	if _, ok := addrPortFromNodeAddr(krpc.NodeAddr{IP: net.IPv4(198, 51, 100, 2), Port: 0}); ok {
		t.Fatal("port 0 accepted")
	}
	if _, ok := addrPortFromNodeAddr(krpc.NodeAddr{IP: net.IPv4(198, 51, 100, 2), Port: 70000}); ok {
		t.Fatal("out-of-range port accepted")
	}
}

// The cache must stay bounded under a discovery-rate flood of
// distinct hashes: entries never exceed the cap no matter how many
// adds arrive.
func TestPeerHintCacheBoundedUnderFlood(t *testing.T) {
	c := &peerHintCache{maxEntries: 128}
	for i := 0; i < 5000; i++ {
		var ih [20]byte
		ih[0] = byte(i >> 8)
		ih[1] = byte(i)
		c.add(ih, hintAddr(6881))
	}
	if c.len() > 128 {
		t.Fatalf("cache holds %d entries under flood, cap is 128", c.len())
	}
}

// TestPeerHintCacheRingBounded: the eviction ring used to grow by
// one slot per distinct inserted hash and was only drained when
// the map hit its entry cap. In the consume-as-you-go regime (the
// production norm: hints are taken by the queue bridge almost
// immediately) the map stays far below the cap, so the ring grew
// without bound — a slow leak under a bounded map. Churn far past
// the cap with immediate takes and assert the ring stays bounded,
// live hints still work, and cap eviction still removes the oldest.
func TestPeerHintCacheRingBounded(t *testing.T) {
	c := &peerHintCache{maxEntries: 8, maxPeers: 2, ttl: time.Hour}
	hash := func(i int) [20]byte {
		var ih [20]byte
		ih[0] = byte(i >> 8)
		ih[1] = byte(i)
		ih[19] = 0xCD
		return ih
	}
	for i := 0; i < 5000; i++ {
		ih := hash(i)
		c.add(ih, hintAddr(6881))
		if got := c.take(ih); len(got) != 1 {
			t.Fatalf("take after add #%d = %v, want 1 hint", i, got)
		}
		if len(c.ring) > 2*c.maxEntries+1 {
			t.Fatalf("ring len = %d at insert %d, want <= %d", len(c.ring), i, 2*c.maxEntries+1)
		}
	}
	// Eviction still works after compaction: fill past the cap
	// without taking, the oldest live entry is evicted.
	for i := 0; i < c.maxEntries; i++ {
		c.add(hash(10000+i), hintAddr(6881))
	}
	c.add(hash(20000), hintAddr(6881))
	if got := c.take(hash(10000)); got != nil {
		t.Fatalf("oldest entry survived cap eviction: %v", got)
	}
	if got := c.take(hash(20000)); len(got) != 1 {
		t.Fatalf("newest entry = %v, want 1 hint", got)
	}
}
