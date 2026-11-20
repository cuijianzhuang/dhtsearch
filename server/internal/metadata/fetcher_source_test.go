package metadata

import (
	"context"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// TestRunQueueSourceAttribution drives a mixed-source batch through
// the queue path: a malformed hash tagged scrape must land on the
// scrape row as a failure, and peerless valid hashes tagged dht and
// feed must land on their own rows as prescreen drops — while the
// global counters keep counting the same outcomes untagged.
func TestRunQueueSourceAttribution(t *testing.T) {
	dest := loopbackClient(t)
	f := &Fetcher{clients: []*torrent.Client{dest}, cfg: Config{
		Workers: 2, Timeout: 5 * time.Second, MaxFiles: 50,
		PrescreenEnabled: true, PrescreenTimeout: 200 * time.Millisecond,
	}}
	q := NewQueue(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.RunQueue(ctx, q, func(Record) { t.Error("dead hash produced a record") })

	q.Push(Item{Hash: "not-a-hash", Source: SourceScrape})
	q.Push(Item{Hash: metainfo.HashBytes([]byte{1, 42}).HexString(), Source: SourceDHT})
	q.Push(Item{Hash: metainfo.HashBytes([]byte{2, 42}).HexString(), Source: SourceFeed})

	deadline := time.Now().Add(5 * time.Second)
	var stats []FetchSourceStats
	for time.Now().Before(deadline) {
		stats = f.SourceStats()
		total := int64(0)
		for _, s := range stats {
			total += s.Attempts
		}
		if total == 3 && stats[0].PrescreenDropped == 1 && stats[1].Failed == 1 && stats[2].PrescreenDropped == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	want := []FetchSourceStats{
		{Source: SourceDHT, Attempts: 1, PrescreenDropped: 1},
		{Source: SourceScrape, Attempts: 1, Failed: 1},
		{Source: SourceFeed, Attempts: 1, PrescreenDropped: 1},
	}
	for i, w := range want {
		if stats[i] != w {
			t.Fatalf("row %d = %+v, want %+v (all rows: %+v)", i, stats[i], w, stats)
		}
	}
	// The global counters agree with the per-source sums.
	if _, _, failed := f.Stats(); failed != 1 {
		t.Fatalf("global failed = %d, want 1", failed)
	}
	if pd := f.PrescreenDropped(); pd != 2 {
		t.Fatalf("global prescreen_dropped = %d, want 2", pd)
	}
}
