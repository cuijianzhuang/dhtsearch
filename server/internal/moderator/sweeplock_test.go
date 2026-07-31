package moderator

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Nothing claims the rows a sweep is working on — Unreviewed just selects
// reviewed_at = 0 — so two concurrent sweeps would classify the same batch
// twice, paying the model twice and double-counting the result. The guard has
// to live in the moderator, because the periodic timer and the admin console
// are different callers.
func TestConcurrentSweepsAreSerialized(t *testing.T) {
	st := newStore(t)
	seed(t, st, "Big Buck Bunny 1080p", "Sintel 2010")

	var inFlight atomic.Int32
	var maxSeen atomic.Int32
	release := make(chan struct{})
	srv := fakeAPI(t, func(string) string {
		n := inFlight.Add(1)
		for {
			old := maxSeen.Load()
			if n <= old || maxSeen.CompareAndSwap(old, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		return "ok"
	}, nil)
	m := newMod(t, st, srv.URL, nil)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = m.SweepOnce(context.Background())
		}(i)
	}
	// Give both goroutines time to reach the guard, then let the model reply.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := maxSeen.Load(); got > 1 {
		t.Errorf("%d classify calls overlapped; sweeps must not run concurrently", got)
	}
	var busy int
	for _, err := range errs {
		if errors.Is(err, ErrSweepInProgress) {
			busy++
		} else if err != nil {
			t.Fatalf("unexpected sweep error: %v", err)
		}
	}
	if busy != 1 {
		t.Errorf("%d callers were told a sweep was in progress, want exactly 1", busy)
	}
}

// The guard must be released afterwards, or the first sweep would be the only
// one this process ever runs.
func TestSweepGuardReleased(t *testing.T) {
	st := newStore(t)
	seed(t, st, "Sintel 2010")
	srv := fakeAPI(t, func(string) string { return "ok" }, nil)
	m := newMod(t, st, srv.URL, nil)

	for i := 0; i < 3; i++ {
		if _, err := m.SweepOnce(context.Background()); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
	}
}
