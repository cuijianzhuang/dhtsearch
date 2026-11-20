package crawler

import (
	"context"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
)

// A node in a failure cooldown must not consume sampler slots: the
// refill keeps offering the whole routing table, and enqueue is the
// gate that keeps banned nodes out of the queue.
func TestEnqueueNodeSkipsBanned(t *testing.T) {
	c := testCrawler()
	bad := addr(7001)
	for i := 0; i < banThreshold; i++ {
		c.quality.Record(bad.String(), false, 0)
	}
	c.enqueueNode(bad)
	if len(c.nodeQ) != 0 {
		t.Fatalf("queued=%d, want 0 (node is banned)", len(c.nodeQ))
	}
	good := addr(7002)
	c.enqueueNode(good)
	if len(c.nodeQ) != 1 {
		t.Fatalf("queued=%d, want 1 (unbanned node still offered)", len(c.nodeQ))
	}
}

// The resample interval scales with the score: proven nodes come due
// sooner than neutral ones, failing ones later. (The neutral case —
// factor exactly 1 — is pinned by TestScheduleResampleClampsInterval.)
func TestScheduleResampleScalesByScore(t *testing.T) {
	c := testCrawler()
	sec := func(n int64) *int64 { return &n }

	delay := func(na krpc.NodeAddr) time.Duration {
		before := time.Now()
		c.scheduleResample(na, sec(600)) // advertised 10 minutes
		c.nodeMu.Lock()
		next := c.nodeNext[na.String()]
		c.nodeMu.Unlock()
		return next.Sub(before)
	}

	good := addr(7101)
	for i := 0; i < 9; i++ {
		c.quality.Record(good.String(), true, 20*time.Millisecond)
	}
	bad := addr(7102)
	for i := 0; i < 4; i++ { // 4 < banThreshold: scored down but not banned
		c.quality.Record(bad.String(), false, 0)
	}
	neutral := addr(7103)

	dGood, dNeutral, dBad := delay(good), delay(neutral), delay(bad)
	if !(dGood < dNeutral && dNeutral < dBad) {
		t.Fatalf("delays good=%v neutral=%v bad=%v, want good < neutral < bad", dGood, dNeutral, dBad)
	}
	if dNeutral < 10*time.Minute-time.Second || dNeutral > 10*time.Minute+time.Second {
		t.Fatalf("neutral delay=%v, want the unscaled advertised 10m", dNeutral)
	}
}

// End-to-end persistence wiring at the crawler level, with fake hooks
// standing in for the store: a start must restore the saved nodes
// into the tracker (Stats reports them), and Close must write a final
// snapshot containing them. The DHT server runs on a loopback UDP
// socket; outbound DHT traffic goes nowhere in this environment,
// which is fine — nothing here depends on an answer.
func TestStartRestoresAndCloseSaves(t *testing.T) {
	saved := []PersistedNode{
		{Addr: "127.0.0.1:29991", SuccW: 10, FailW: 1, LatencyMs: 40, Attempts: 9, UpdatedAt: time.Now().Unix()},
		{Addr: "127.0.0.1:29992", SuccW: 5, FailW: 2, LatencyMs: 90, Attempts: 6, UpdatedAt: time.Now().Unix()},
	}
	var mu sync.Mutex
	var snapshot []PersistedNode
	saves := 0
	c, err := Start(Config{
		Port:     0,
		Enabled:  true,
		Samplers: 1,
		Logger:   log.New(io.Discard, "", 0),
		LoadSeedNodes: func(ctx context.Context) ([]PersistedNode, error) {
			return saved, nil
		},
		SaveSeedNodes: func(ctx context.Context, nodes []PersistedNode) error {
			mu.Lock()
			defer mu.Unlock()
			saves++
			snapshot = nodes
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := c.Stats()
	if st.RestoredNodes != 2 {
		t.Fatalf("RestoredNodes=%d, want 2", st.RestoredNodes)
	}
	if st.TrackedNodes < 2 {
		t.Fatalf("TrackedNodes=%d, want >= 2 (restored nodes are tracked)", st.TrackedNodes)
	}
	if got := c.quality.Score("127.0.0.1:29991"); got < 0.85 {
		t.Fatalf("restored node score=%v, want its saved ~0.91", got)
	}

	c.Close()
	mu.Lock()
	defer mu.Unlock()
	if saves == 0 {
		t.Fatal("Close did not save a final snapshot")
	}
	found := map[string]bool{}
	for _, n := range snapshot {
		found[n.Addr] = true
	}
	for _, want := range []string{"127.0.0.1:29991", "127.0.0.1:29992"} {
		if !found[want] {
			t.Fatalf("final snapshot %v is missing restored node %s", snapshot, want)
		}
	}
}

// A crawler started disabled holds an empty tracker; its Close must
// not save (in production that would wipe the previous run's snapshot
// from the database).
func TestDisabledCrawlerCloseDoesNotSave(t *testing.T) {
	saves := 0
	c, err := Start(Config{
		Enabled: false,
		Logger:  log.New(io.Discard, "", 0),
		SaveSeedNodes: func(ctx context.Context, nodes []PersistedNode) error {
			saves++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	c.Close()
	if saves != 0 {
		t.Fatalf("disabled crawler saved %d times, want 0", saves)
	}
}
