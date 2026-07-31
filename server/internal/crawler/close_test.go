package crawler

import (
	"io"
	"log"
	"sync"
	"testing"
)

// Cancelling the crawler does not stop its samplers synchronously: one already
// inside a query pushes its results after Close returns. Before the close flag
// that was a send on a closed channel, which panics and takes the process down
// on every shutdown that happened to land badly. Run under -race.
func TestClosePushRace(t *testing.T) {
	c := &Crawler{
		logger: log.New(io.Discard, "", 0),
		seen:   make(map[[20]byte]struct{}, seenCapacity),
		out:    make(chan string, 8),
	}
	// Drain, so pushes are not merely bouncing off a full buffer.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range c.out {
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(base byte) {
			defer wg.Done()
			for n := 0; n < 500; n++ {
				var ih [20]byte
				ih[0], ih[1] = base, byte(n)
				c.push(ih)
			}
		}(byte(i))
	}
	c.Close()
	wg.Wait()
	<-done

	// Idempotent: a second Close must not close the channel twice.
	c.Close()
}

// Close on a disabled crawler has no channel to close and must stay a no-op.
func TestCloseDisabledCrawler(t *testing.T) {
	c, err := Start(Config{Enabled: false, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	c.Close()
	if c.Infohashes() != nil {
		t.Error("disabled crawler must expose a nil channel")
	}
}
