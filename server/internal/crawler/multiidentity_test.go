package crawler

import (
	"context"
	"encoding/hex"
	"io"
	"log"
	"net"
	"sync"
	"testing"
	"time"
)

func TestClampIdentities(t *testing.T) {
	cases := []struct{ in, want int }{
		{-3, 1}, {0, 1}, {1, 1}, {2, 2}, {16, 16}, {17, 16}, {1000, 16},
	}
	for _, tc := range cases {
		if got := clampIdentities(tc.in); got != tc.want {
			t.Fatalf("clampIdentities(%d)=%d, want %d", tc.in, got, tc.want)
		}
	}
}

// freePorts returns n consecutive UDP ports that are all free right
// now, starting the search from a random one. The sockets are closed
// before returning, so there is an inherent race with other binds;
// the retry loop makes it a non-issue on a test machine.
func freePorts(t *testing.T, n int) int {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		probe, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
		if err != nil {
			t.Fatalf("probe listen: %v", err)
		}
		base := probe.LocalAddr().(*net.UDPAddr).Port
		probe.Close()
		if base+n-1 > 65535 {
			continue
		}
		var held []*net.UDPConn
		ok := true
		for i := 0; i < n; i++ {
			conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: base + i})
			if err != nil {
				ok = false
				break
			}
			held = append(held, conn)
		}
		for _, conn := range held {
			conn.Close()
		}
		if ok {
			return base
		}
	}
	t.Fatalf("no %d consecutive free UDP ports found", n)
	return 0
}

// Three identities on loopback: distinct node IDs on consecutive
// ports, the shared seed set restored into every identity's own
// tracker and sampler queue, and aggregate Stats that sum the
// identities while keeping RestoredNodes as the distinct loaded set.
//
// The wire-level harvest path (a foreign get_peers packet landing in
// OnQuery) cannot be exercised in this sandbox: outbound UDP is
// blocked outright, even to 127.0.0.1. What can be pinned here is
// the funnel every identity's query/announce/sample paths end in —
// Crawler.push and its single deduplicated channel, covered directly
// below and in crawler_test.go.
func TestStartMultipleIdentities(t *testing.T) {
	base := freePorts(t, 3)
	saved := []PersistedNode{
		{Addr: "127.0.0.1:29991", SuccW: 10, FailW: 1, LatencyMs: 40, Attempts: 9, UpdatedAt: time.Now().Unix()},
		{Addr: "127.0.0.1:29992", SuccW: 5, FailW: 2, LatencyMs: 90, Attempts: 6, UpdatedAt: time.Now().Unix()},
	}
	c, err := Start(Config{
		Port:       base,
		Enabled:    true,
		Samplers:   1,
		Identities: 3,
		Logger:     log.New(io.Discard, "", 0),
		LoadSeedNodes: func(ctx context.Context) ([]PersistedNode, error) {
			return saved, nil
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	if len(c.identities) != 3 {
		t.Fatalf("identities=%d, want 3", len(c.identities))
	}
	ids := map[[20]byte]int{}
	for i, id := range c.identities {
		if got := id.server.Addr().(*net.UDPAddr).Port; got != base+i {
			t.Fatalf("identity %d on port %d, want %d", i, got, base+i)
		}
		nid := id.server.ID()
		if prev, dup := ids[nid]; dup {
			t.Fatalf("identities %d and %d share node ID %x", prev, i, nid)
		}
		ids[nid] = i
		if id.restored != len(saved) {
			t.Fatalf("identity %d restored=%d, want %d (shared seed set)", i, id.restored, len(saved))
		}
		// Its own tracker resumed the saved scores…
		if got := id.quality.Score("127.0.0.1:29991"); got < 0.85 {
			t.Fatalf("identity %d restored node score=%v, want its saved ~0.91", i, got)
		}
		// …and its own sampler queue was offered the seed nodes
		// (enqueueNode leaves a resample reservation per address).
		id.nodeMu.Lock()
		for _, want := range []string{"127.0.0.1:29991", "127.0.0.1:29992"} {
			if _, ok := id.nodeNext[want]; !ok {
				t.Fatalf("identity %d never enqueued seed node %s", i, want)
			}
		}
		id.nodeMu.Unlock()
	}

	st := c.Stats()
	if st.Identities != 3 {
		t.Fatalf("Stats.Identities=%d, want 3", st.Identities)
	}
	if st.RestoredNodes != len(saved) {
		t.Fatalf("Stats.RestoredNodes=%d, want %d (distinct persisted nodes, not per-identity sum)",
			st.RestoredNodes, len(saved))
	}
	// Every identity tracks the seed set independently.
	if st.TrackedNodes < 3*len(saved) {
		t.Fatalf("Stats.TrackedNodes=%d, want at least %d (3 identities × %d seeds)",
			st.TrackedNodes, 3*len(saved), len(saved))
	}

	// The shared funnel: hashes pushed on behalf of any identity
	// land on the one channel, deduplicated across identities.
	for i := 0; i < 3; i++ {
		var ih [20]byte
		ih[0] = 0xA0 + byte(i)
		ih[19] = byte(i + 1)
		c.identities[i].c.push(ih)
	}
	for i := 0; i < 3; i++ {
		var ih [20]byte
		ih[0] = 0xA0 + byte(i)
		ih[19] = byte(i + 1)
		select {
		case got := <-c.Infohashes():
			if want := hex.EncodeToString(ih[:]); got != want {
				t.Fatalf("harvested %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("infohash %d never reached the shared channel", i)
		}
	}
	var dup [20]byte
	dup[0] = 0xA0
	dup[19] = 1
	c.push(dup) // already harvested via identity 0 above
	select {
	case got := <-c.Infohashes():
		t.Fatalf("duplicate infohash re-emitted: %q", got)
	case <-time.After(300 * time.Millisecond):
	}
	if n := c.SeenCount(); n != 3 {
		t.Fatalf("SeenCount=%d, want 3", n)
	}
}

// Failure isolation: an extra identity whose port is taken is logged
// and skipped, but identity 0 losing its port is still fatal.
func TestStartIdentityFailureIsolation(t *testing.T) {
	base := freePorts(t, 3)
	blocker, err := net.ListenUDP("udp", &net.UDPAddr{Port: base + 1})
	if err != nil {
		t.Fatalf("blocker listen: %v", err)
	}
	defer blocker.Close()

	c, err := Start(Config{
		Port:       base,
		Enabled:    true,
		Samplers:   1,
		Identities: 3,
		Logger:     log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("Start with identity 1 blocked: %v", err)
	}
	if len(c.identities) != 2 {
		t.Fatalf("identities=%d, want 2 (blocked one skipped)", len(c.identities))
	}
	if got := c.Stats().Identities; got != 2 {
		t.Fatalf("Stats.Identities=%d, want 2", got)
	}
	c.Close()

	// Fresh ports for the fatal case: Server.Close releases the
	// socket asynchronously, so the ones above may still be held.
	base = freePorts(t, 3)
	blocker0, err := net.ListenUDP("udp", &net.UDPAddr{Port: base})
	if err != nil {
		t.Fatalf("blocker0 listen: %v", err)
	}
	defer blocker0.Close()
	if _, err := Start(Config{
		Port:       base,
		Enabled:    true,
		Samplers:   1,
		Identities: 3,
		Logger:     log.New(io.Discard, "", 0),
	}); err == nil {
		t.Fatal("Start with identity 0 blocked: nil error, want fatal")
	}
}

// The persistence snapshot is merged, not per-identity: with two
// identities tracking different proven nodes (and one node in
// common), Close must save the union once, the common node collapsed
// to the entry with the most observations — never two replace-all
// writes racing each other.
func TestCloseSavesMergedSnapshot(t *testing.T) {
	base := freePorts(t, 2)
	var mu sync.Mutex
	var snapshots [][]PersistedNode
	c, err := Start(Config{
		Port:       base,
		Enabled:    true,
		Samplers:   1,
		Identities: 2,
		Logger:     log.New(io.Discard, "", 0),
		SaveSeedNodes: func(ctx context.Context, nodes []PersistedNode) error {
			mu.Lock()
			defer mu.Unlock()
			cp := make([]PersistedNode, len(nodes))
			copy(cp, nodes)
			snapshots = append(snapshots, cp)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	prove := func(id *identity, addrStr string, times int) {
		for i := 0; i < times; i++ {
			id.quality.Record(addrStr, true, 20*time.Millisecond)
		}
	}
	prove(c.identities[0], "127.0.0.1:31001", 5)
	prove(c.identities[0], "127.0.0.1:31003", 3) // shared, less evidence
	prove(c.identities[1], "127.0.0.1:31002", 6)
	prove(c.identities[1], "127.0.0.1:31003", 9) // shared, more evidence

	c.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(snapshots) != 1 {
		t.Fatalf("saves=%d, want exactly 1 merged snapshot at Close", len(snapshots))
	}
	byAddr := map[string]PersistedNode{}
	for _, n := range snapshots[0] {
		byAddr[n.Addr] = n
	}
	if len(byAddr) != 3 {
		t.Fatalf("snapshot has %d nodes, want the union of 3: %v", len(byAddr), snapshots[0])
	}
	if got := byAddr["127.0.0.1:31003"].Attempts; got != 9 {
		t.Fatalf("shared node attempts=%d, want 9 (most-observed entry wins)", got)
	}
}
