package metadata

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func hashN(n int) string {
	return fmt.Sprintf("%040x", n+1)
}

func TestQueueOrdering(t *testing.T) {
	q := NewQueue(0)
	t0 := time.Now()
	t1 := t0.Add(time.Minute)
	items := []Item{
		{Hash: hashN(1), Seeders: 5, Source: SourceDHT, At: t0},   // a
		{Hash: hashN(2), Seeders: 50, Source: SourceDHT, At: t0},  // b
		{Hash: hashN(3), Seeders: 50, Source: SourceFeed, At: t0}, // c
		{Hash: hashN(4), Seeders: 50, Source: SourceFeed, At: t1}, // d: fresher than c
		{Hash: hashN(5), Seeders: -1, Source: SourceFeed, At: t1}, // e: unknown = 0 seeders
		{Hash: hashN(6), Seeders: 0, Source: SourceDHT, At: t1},   // f
	}
	for _, it := range items {
		if !q.Push(it) {
			t.Fatalf("push %s refused", it.Hash)
		}
	}
	want := []string{hashN(4), hashN(3), hashN(2), hashN(1), hashN(5), hashN(6)}
	ctx := context.Background()
	for i, w := range want {
		it, ok := q.Pop(ctx)
		if !ok {
			t.Fatalf("pop %d: queue empty", i)
		}
		if it.Hash != w {
			t.Fatalf("pop %d = %s, want %s", i, it.Hash, w)
		}
	}
	if st := q.Stats(); st.Pushed != 6 || st.Depth != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestQueueDedup(t *testing.T) {
	q := NewQueue(0)
	it := Item{Hash: hashN(1), Seeders: 3, Source: SourceDHT}
	if !q.Push(it) {
		t.Fatal("first push refused")
	}
	if q.Push(it) {
		t.Fatal("duplicate push accepted")
	}
	if st := q.Stats(); st.Deduped != 1 || st.Depth != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if _, ok := q.Pop(context.Background()); !ok {
		t.Fatal("pop failed")
	}
	// After the pop the hash is free again.
	if !q.Push(it) {
		t.Fatal("push after pop refused")
	}
}

func TestQueueCapEviction(t *testing.T) {
	q := NewQueue(3)
	push := func(n int, seeders int32) bool {
		return q.Push(Item{Hash: hashN(n), Seeders: seeders, Source: SourceDHT, At: time.Now()})
	}
	push(1, 1)
	push(2, 2)
	push(3, 3)
	if !push(4, 100) {
		t.Fatal("high-seeder push into full queue refused")
	}
	if st := q.Stats(); st.Evicted != 1 || st.Depth != 3 {
		t.Fatalf("stats = %+v", st)
	}
	if push(5, 0) {
		t.Fatal("low-seeder push into full queue accepted")
	}
	if st := q.Stats(); st.Dropped != 1 {
		t.Fatalf("stats = %+v", st)
	}
	ctx := context.Background()
	for _, w := range []string{hashN(4), hashN(3), hashN(2)} {
		it, ok := q.Pop(ctx)
		if !ok || it.Hash != w {
			t.Fatalf("pop = %v, %v; want %s", it, ok, w)
		}
	}
}

func TestQueuePopBlocksAndCtx(t *testing.T) {
	q := NewQueue(0)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, ok := q.Pop(ctx); ok {
		t.Fatal("pop from empty queue succeeded")
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		q.Push(Item{Hash: hashN(9), Source: SourceDHT})
	}()
	it, ok := q.Pop(context.Background())
	if !ok || it.Hash != hashN(9) {
		t.Fatalf("pop = %v, %v", it, ok)
	}
}

// TestQueueConcurrent hammers Push/Pop from many goroutines; it exists
// mainly so -race has something to bite on.
func TestQueueConcurrent(t *testing.T) {
	q := NewQueue(4096)
	const producers = 8
	const perProducer = 250
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var popped atomic.Int64
	var popWG sync.WaitGroup
	for i := 0; i < 4; i++ {
		popWG.Add(1)
		go func() {
			defer popWG.Done()
			for {
				if _, ok := q.Pop(ctx); !ok {
					return
				}
				popped.Add(1)
			}
		}()
	}
	var pushWG sync.WaitGroup
	for p := 0; p < producers; p++ {
		pushWG.Add(1)
		go func(p int) {
			defer pushWG.Done()
			for i := 0; i < perProducer; i++ {
				q.Push(Item{
					Hash:    fmt.Sprintf("%040x", p*perProducer+i+1),
					Seeders: int32(i % 7),
					Source:  SourceDHT,
					At:      time.Now(),
				})
			}
		}(p)
	}
	pushWG.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for popped.Load() < producers*perProducer && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	popWG.Wait()
	if got := popped.Load(); got != producers*perProducer {
		t.Fatalf("popped %d, want %d (stats %+v)", got, producers*perProducer, q.Stats())
	}
}
