package crawler

import (
	"encoding/hex"
	"io"
	"log"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
)

// The IPv6 endpoint tests need to bind a udp6 socket. Hosts without
// IPv6 at all are exactly the production case the graceful fallback
// covers (and TestStartIPv6GracefulWhenSocketTaken pins that path
// without needing a v6-less host), so where even a probe bind fails
// the dual-stack assertions skip instead of failing.
func requireUDP6(t *testing.T) {
	t.Helper()
	conn, err := net.ListenUDP("udp6", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Skipf("udp6 bind unavailable in this environment: %v", err)
	}
	conn.Close()
}

func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// With IPv6 enabled, every identity runs two servers on the SAME
// port number: the IPv4 one on a udp4 socket and the IPv6 one on a
// udp6 socket, under one shared node ID per identity.
func TestStartIPv6DualStackEndpoints(t *testing.T) {
	requireUDP6(t)
	base := freePorts(t, 2)
	c, err := Start(Config{
		Port:        base,
		Enabled:     true,
		Samplers:    1,
		Identities:  2,
		IPv6Enabled: true,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	if len(c.identities) != 2 {
		t.Fatalf("identities=%d, want 2", len(c.identities))
	}
	ids := map[[20]byte]int{}
	for i, id := range c.identities {
		if len(id.eps) != 2 {
			t.Fatalf("identity %d endpoints=%d, want 2 (IPv4 + IPv6)", i, len(id.eps))
		}
		ep4, ep6 := id.eps[0], id.eps[1]
		if ep4.v6 || !ep6.v6 {
			t.Fatalf("identity %d endpoint families = (%v, %v), want (v4, v6)", i, ep4.v6, ep6.v6)
		}
		a4 := ep4.server.Addr().(*net.UDPAddr)
		a6 := ep6.server.Addr().(*net.UDPAddr)
		if a4.Port != base+i || a6.Port != base+i {
			t.Fatalf("identity %d ports = (%d, %d), want both %d", i, a4.Port, a6.Port, base+i)
		}
		if a4.IP.To4() == nil {
			t.Fatalf("identity %d IPv4 endpoint bound to non-IPv4 %v", i, a4.IP)
		}
		if a6.IP.To4() != nil {
			t.Fatalf("identity %d IPv6 endpoint bound to non-IPv6 %v", i, a6.IP)
		}
		if ep4.server.ID() != ep6.server.ID() {
			t.Fatalf("identity %d endpoints have different node IDs (one identity = one node)", i)
		}
		nid := ep4.server.ID()
		if prev, dup := ids[nid]; dup {
			t.Fatalf("identities %d and %d share node ID %x", prev, i, nid)
		}
		ids[nid] = i
		// The identity's long-standing fields alias the IPv4 endpoint.
		if id.server != ep4.server || id.nodeQ != ep4.nodeQ || id.nodeNext == nil {
			t.Fatalf("identity %d fields do not alias its IPv4 endpoint", i)
		}
	}

	st := c.Stats()
	if !st.IPv6Enabled {
		t.Fatal("Stats.IPv6Enabled=false, want true")
	}
	if st.IPv6Bound != 2 {
		t.Fatalf("Stats.IPv6Bound=%d, want 2", st.IPv6Bound)
	}
}

// IPv6 is opt-out, not opt-in: with the flag off (the zero value the
// historical callers pass) an identity is exactly one IPv4 endpoint.
func TestStartIPv6DisabledByDefault(t *testing.T) {
	base := freePorts(t, 1)
	c, err := Start(Config{
		Port:       base,
		Enabled:    true,
		Samplers:   1,
		Identities: 1,
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()
	if len(c.identities[0].eps) != 1 {
		t.Fatalf("endpoints=%d, want 1", len(c.identities[0].eps))
	}
	st := c.Stats()
	if st.IPv6Enabled || st.IPv6Bound != 0 {
		t.Fatalf("Stats IPv6 = (enabled %v, bound %d), want (false, 0)", st.IPv6Enabled, st.IPv6Bound)
	}
}

// The graceful path: the udp6 socket for identity 0's port is
// already taken, so IPv6 cannot bind. Start must still succeed on
// IPv4 alone — an IPv6 failure is never fatal — and Stats must
// report enabled-but-unbound so the operator can see the fallback.
func TestStartIPv6GracefulWhenSocketTaken(t *testing.T) {
	requireUDP6(t)
	base := freePorts(t, 2)
	held, err := net.ListenUDP("udp6", &net.UDPAddr{Port: base})
	if err != nil {
		t.Fatalf("hold udp6 port: %v", err)
	}
	defer held.Close()

	c, err := Start(Config{
		Port:        base,
		Enabled:     true,
		Samplers:    1,
		Identities:  2,
		IPv6Enabled: true,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("Start with unbindable IPv6: %v (must degrade, not fail)", err)
	}
	defer c.Close()
	for i, id := range c.identities {
		if id.v6Endpoint() != nil {
			t.Fatalf("identity %d has an IPv6 endpoint, want none after the fallback latched", i)
		}
	}
	st := c.Stats()
	if !st.IPv6Enabled {
		t.Fatal("Stats.IPv6Enabled=false, want true (it was configured)")
	}
	if st.IPv6Bound != 0 {
		t.Fatalf("Stats.IPv6Bound=%d, want 0", st.IPv6Bound)
	}
}

// Node offers route by address family: a node lands in the resample
// schedule of the endpoint whose socket can query it, and an IPv6
// node offered to an IPv4-only identity goes nowhere.
func TestEnqueueRoutesByFamily(t *testing.T) {
	requireUDP6(t)
	base := freePorts(t, 1)
	c, err := Start(Config{
		Port:        base,
		Enabled:     true,
		Samplers:    1,
		Identities:  1,
		IPv6Enabled: true,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()
	id := c.identities[0]
	ep4, ep6 := id.eps[0], id.eps[1]

	v4 := krpc.NodeAddr{IP: net.IPv4(10, 9, 8, 7), Port: 6881}
	v6 := krpc.NodeAddr{IP: net.ParseIP("2001:db8::7"), Port: 6881}
	id.enqueueNode(v4)
	id.enqueueNode(v6)

	ep4.nodeMu.Lock()
	_, in4v4 := ep4.nodeNext[v4.String()]
	_, in6v4 := ep4.nodeNext[v6.String()]
	ep4.nodeMu.Unlock()
	ep6.nodeMu.Lock()
	_, in4v6 := ep6.nodeNext[v4.String()]
	_, in6v6 := ep6.nodeNext[v6.String()]
	ep6.nodeMu.Unlock()
	if !in4v4 || in6v4 {
		t.Fatalf("IPv4 node scheduled on (v4 %v, v6 %v), want (true, false)", in4v4, in6v4)
	}
	if !in6v6 || in4v6 {
		t.Fatalf("IPv6 node scheduled on (v4 %v, v6 %v), want (false, true)", in4v6, in6v6)
	}

	// IPv4-only identity: the IPv6 offer is dropped, not queued.
	c4, err := Start(Config{
		Port:       freePorts(t, 1),
		Enabled:    true,
		Samplers:   1,
		Identities: 1,
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("Start v4-only: %v", err)
	}
	defer c4.Close()
	id4 := c4.identities[0]
	id4.enqueueNode(v6)
	id4.nodeMu.Lock()
	_, leaked := id4.nodeNext[v6.String()]
	id4.nodeMu.Unlock()
	if leaked || len(id4.nodeQ) != 0 {
		t.Fatal("IPv6 node leaked into an IPv4-only identity's sampler state")
	}
}

// SeenV6 counts first discoveries attributed to an IPv6 source, and
// only first discoveries: a repeat sighting — from either family —
// does not count again.
func TestPushFromAttributesSeenV6(t *testing.T) {
	c := testCrawler()
	var ih1, ih2, ih3 [20]byte
	ih1[0], ih2[0], ih3[0] = 0x61, 0x62, 0x63

	c.pushFrom(ih1, true)
	c.pushFrom(ih1, true)  // duplicate: not a discovery
	c.pushFrom(ih2, false) // IPv4 discovery
	c.push(ih3)            // unattributed counts as IPv4

	if got := c.Stats().SeenV6; got != 1 {
		t.Fatalf("SeenV6=%d, want 1", got)
	}
	if got := c.SeenCount(); got != 3 {
		t.Fatalf("SeenCount=%d, want 3", got)
	}
}

// The merged persistence snapshot is family-blind (addresses are
// strings); each endpoint's bootstrap picks out its own family's
// share, in both directions.
func TestSeedAddrsForFamily(t *testing.T) {
	seeds := []string{
		"127.0.0.1:6881",
		"[2001:db8::5]:6881",
		"[::1]:9",
		"not-an-address",
		"",
	}
	v4 := seedAddrsForFamily(seeds, false)
	if len(v4) != 1 || v4[0].String() != "127.0.0.1:6881" {
		t.Fatalf("v4 seeds = %v, want [127.0.0.1:6881]", v4)
	}
	v6 := seedAddrsForFamily(seeds, true)
	got := map[string]bool{}
	for _, a := range v6 {
		got[a.String()] = true
	}
	if len(v6) != 2 || !got["[2001:db8::5]:6881"] || !got["[::1]:9"] {
		t.Fatalf("v6 seeds = %v, want the two IPv6 entries", v6)
	}
}

// A persisted IPv6 node survives the quality snapshot round-trip:
// Seed restores it and Top offers it back for the next save,
// unchanged — the snapshot format needs no family marker.
func TestQualitySnapshotRoundTripsIPv6(t *testing.T) {
	q := newNodeQuality()
	addr := "[2001:db8::9]:6881"
	q.Seed([]PersistedNode{
		{Addr: addr, SuccW: 10, FailW: 1, LatencyMs: 30, Attempts: 9, UpdatedAt: time.Now().Unix()},
	})
	top := q.Top(512, 0.5)
	found := false
	for _, n := range top {
		if n.Addr == addr {
			found = true
		}
	}
	if !found {
		t.Fatalf("Top = %v, want the IPv6 node %s in it", top, addr)
	}
}

// The hint cache is family-agnostic end to end: an IPv6 hint taken
// from DHT traffic is stored and handed to the fetcher unchanged.
// (The address conversions themselves are covered in
// peerhints_test.go.)
func TestPeerHintsIPv6RoundTrip(t *testing.T) {
	c := &Crawler{}
	var ih [20]byte
	ih[0] = 0x77
	hash := hex.EncodeToString(ih[:])
	want := netip.MustParseAddrPort("[2001:db8::7]:6881")
	c.AddPeerHint(hash, want)
	got := c.TakePeerHints(hash)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("TakePeerHints = %v, want [%v]", got, want)
	}
	if again := c.TakePeerHints(hash); again != nil {
		t.Fatalf("second TakePeerHints = %v, want nil (consumed)", again)
	}
}

// The starting-nodes composition for the IPv6 side must never error
// out, even with no usable seeds at all: an error there would put
// the library's table maintainer into a retry loop over a condition
// (no IPv6 bootstrap reachable) that is not a failure. Resolution of
// the public routers depends on DNS, so only the contract — no
// error, and any returned address is IPv6 — is asserted.
func TestStartingNodesIPv6NeverErrors(t *testing.T) {
	out, err := startingNodes(nil, true)
	if err != nil {
		t.Fatalf("startingNodes(nil, v6): %v", err)
	}
	for _, a := range out {
		if a.IP().To4() != nil {
			t.Fatalf("IPv6 starting nodes contain IPv4 address %v", a)
		}
	}
}
