package scraper

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"sync"
	"testing"
	"time"
)

// hashN builds a valid distinct hex infohash from n.
func hashN(n byte) string {
	var ih [20]byte
	ih[0] = n
	ih[19] = 1 // never the all-zero hash
	return hex.EncodeToString(ih[:])
}

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// fakeSeeders installs a scrape stub that reports seeders[hash] for known
// hashes and 0 otherwise.
func fakeSeeders(s *Scraper, seeders map[string]int32) {
	s.scrapeTracker = func(_ context.Context, _ *trackerConn, ihs [][20]byte) ([]SwarmSample, error) {
		out := make([]SwarmSample, len(ihs))
		for i, ih := range ihs {
			out[i] = SwarmSample{Seeders: seeders[hex.EncodeToString(ih[:])]}
		}
		return out, nil
	}
}

func recvOrFail(t *testing.T, ch <-chan Item) string {
	t.Helper()
	select {
	case it := <-ch:
		return it.Hash
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a prioritized hash")
		return ""
	}
}

func TestNewRejectsUnusableTrackers(t *testing.T) {
	_, err := New(Config{
		Trackers: []string{"https://tracker.example/announce", "not a url at all", "udp://"},
		Logger:   quietLogger(),
	})
	if err == nil {
		t.Fatal("want error when no tracker is scrapeable")
	}
}

func TestTrackerHost(t *testing.T) {
	host, err := trackerHost("udp://tracker.opentrackr.org:1337/announce")
	if err != nil {
		t.Fatal(err)
	}
	if host != "tracker.opentrackr.org:1337" {
		t.Fatalf("host = %q", host)
	}
	if _, err := trackerHost("http://tracker.example/announce"); err == nil {
		t.Fatal("want error for non-udp scheme")
	}
}

func TestPrioritizesBySeeders(t *testing.T) {
	s, err := New(Config{
		Trackers: []string{"udp://tracker.invalid:1337/announce"},
		BatchMax: 4,
		Logger:   quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	low, mid, high, zero := hashN(1), hashN(2), hashN(3), hashN(4)
	fakeSeeders(s, map[string]int32{low: 2, mid: 40, high: 900})

	in := make(chan string, 4)
	for _, h := range []string{zero, low, high, mid} { // one full batch
		in <- h
	}
	s.Run(context.Background(), in)
	defer s.Close()

	want := []string{high, mid, low, zero}
	for i, w := range want {
		if got := recvOrFail(t, s.Out()); got != w {
			t.Fatalf("position %d: got %s want %s", i, got, w)
		}
	}
	st := s.Stats()
	if st.Scraped != 4 || st.Seeded != 3 {
		t.Fatalf("stats = %+v, want Scraped=4 Seeded=3", st)
	}
}

func TestQueueEvictsLowestPriority(t *testing.T) {
	s, err := New(Config{
		Trackers: []string{"udp://tracker.invalid:1337/announce"},
		BatchMax: 4,
		QueueCap: 2,
		Logger:   quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h1, h2, h3, h4 := hashN(1), hashN(2), hashN(3), hashN(4)
	fakeSeeders(s, map[string]int32{h1: 9, h2: 7, h3: 5, h4: 3})

	in := make(chan string, 4)
	for _, h := range []string{h4, h2, h1, h3} { // one full batch
		in <- h
	}
	close(in) // out closes once everything surviving has been delivered
	s.Run(context.Background(), in)
	defer s.Close()

	// The batch lands in the queue atomically, so the cap keeps the top two.
	var got []string
	for it := range s.Out() {
		got = append(got, it.Hash)
	}
	want := []string{h1, h2}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("delivered %v, want %v", got, want)
	}
	if ev := s.Stats().Evicted; ev != 2 {
		t.Fatalf("Evicted = %d, want 2", ev)
	}
}

func TestPartialBatchFlushesOnTimer(t *testing.T) {
	s, err := New(Config{
		Trackers:  []string{"udp://tracker.invalid:1337/announce"},
		BatchMax:  70,
		BatchWait: 50 * time.Millisecond,
		Logger:    quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := hashN(7)
	fakeSeeders(s, map[string]int32{h: 1})

	in := make(chan string, 1)
	in <- h
	s.Run(context.Background(), in)
	defer s.Close()

	if got := recvOrFail(t, s.Out()); got != h {
		t.Fatalf("got %s want %s", got, h)
	}
}

func TestAllTrackersFailingStillDelivers(t *testing.T) {
	s, err := New(Config{
		Trackers: []string{"udp://tracker.invalid:1337/announce"},
		BatchMax: 2,
		Logger:   quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.scrapeTracker = func(context.Context, *trackerConn, [][20]byte) ([]SwarmSample, error) {
		return nil, fmt.Errorf("tracker down")
	}
	h1, h2 := hashN(1), hashN(2)
	in := make(chan string, 2)
	in <- h1
	in <- h2
	s.Run(context.Background(), in)
	defer s.Close()

	got := map[string]bool{recvOrFail(t, s.Out()): true, recvOrFail(t, s.Out()): true}
	if !got[h1] || !got[h2] {
		t.Fatalf("delivered %v, want both %s and %s", got, h1, h2)
	}
	if errs := s.Stats().ScrapeErrs; errs == 0 {
		t.Fatal("want ScrapeErrs > 0")
	}
}

// TestSinkReceivesPerFieldMax installs two trackers that disagree on
// every field and checks the sink gets the per-field maximum, that an
// all-zero answer from a live tracker is persisted as zeros (a real
// observation, not a gap), and that the queue still ranks by the
// aggregated seeders.
func TestSinkReceivesPerFieldMax(t *testing.T) {
	var mu sync.Mutex
	var sunk []SwarmStat
	s, err := New(Config{
		Trackers: []string{
			"udp://tracker-a.invalid:1337/announce",
			"udp://tracker-b.invalid:1337/announce",
		},
		BatchMax: 2,
		Logger:   quietLogger(),
		Sink: func(stats []SwarmStat) {
			mu.Lock()
			sunk = append(sunk, stats...)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h1, h2 := hashN(11), hashN(12)
	// Tracker A knows h1 well but nothing of h2; tracker B the
	// reverse, with different fields winning on each side.
	byHost := map[string]map[string]SwarmSample{
		"tracker-a.invalid:1337": {
			h1: {Seeders: 10, Leechers: 1, Completed: 100},
			h2: {Seeders: 0, Leechers: 0, Completed: 0},
		},
		"tracker-b.invalid:1337": {
			h1: {Seeders: 3, Leechers: 9, Completed: 40},
			h2: {Seeders: 0, Leechers: 0, Completed: 0},
		},
	}
	s.scrapeTracker = func(_ context.Context, tc *trackerConn, ihs [][20]byte) ([]SwarmSample, error) {
		out := make([]SwarmSample, len(ihs))
		for i, ih := range ihs {
			out[i] = byHost[tc.host][hex.EncodeToString(ih[:])]
		}
		return out, nil
	}

	in := make(chan string, 2)
	in <- h1
	in <- h2
	s.Run(context.Background(), in)
	defer s.Close()

	if got := recvOrFail(t, s.Out()); got != h1 {
		t.Fatalf("queue head = %s, want %s (10 seeders)", got, h1)
	}
	// The sink runs after the queue hand-off; drain the second item
	// too so the round is fully settled before asserting.
	if got := recvOrFail(t, s.Out()); got != h2 {
		t.Fatalf("second = %s, want %s", got, h2)
	}
	// The sink runs on the scrape worker just after the hand-off;
	// poll briefly rather than assume the ordering.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(sunk)
		mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sunk) != 2 {
		t.Fatalf("sink got %d stats, want 2: %v", len(sunk), sunk)
	}
	got := map[string]SwarmStat{}
	for _, st := range sunk {
		got[st.Hash] = st
	}
	want1 := SwarmStat{Hash: h1, Seeders: 10, Leechers: 9, Completed: 100}
	if got[h1] != want1 {
		t.Fatalf("h1 aggregated = %+v, want per-field max %+v", got[h1], want1)
	}
	// h2 was answered all-zero by both trackers: present, with zeros.
	want2 := SwarmStat{Hash: h2}
	if got[h2] != want2 {
		t.Fatalf("h2 aggregated = %+v, want zeros %+v", got[h2], want2)
	}
}

// TestSinkSilentWhenAllTrackersFail: a round nobody answered teaches
// nothing, so the sink must not be called — persisting zeros here
// would fabricate observations and feed the dead-torrent streaks.
func TestSinkSilentWhenAllTrackersFail(t *testing.T) {
	called := make(chan []SwarmStat, 1)
	s, err := New(Config{
		Trackers: []string{"udp://tracker.invalid:1337/announce"},
		BatchMax: 2,
		Logger:   quietLogger(),
		Sink:     func(stats []SwarmStat) { called <- stats },
	})
	if err != nil {
		t.Fatal(err)
	}
	s.scrapeTracker = func(context.Context, *trackerConn, [][20]byte) ([]SwarmSample, error) {
		return nil, fmt.Errorf("tracker down")
	}
	in := make(chan string, 2)
	in <- hashN(1)
	in <- hashN(2)
	s.Run(context.Background(), in)
	defer s.Close()

	recvOrFail(t, s.Out())
	recvOrFail(t, s.Out())
	select {
	case stats := <-called:
		t.Fatalf("sink called with %v despite every tracker failing", stats)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestMalformedHashesAreSkipped(t *testing.T) {
	s, err := New(Config{
		Trackers: []string{"udp://tracker.invalid:1337/announce"},
		BatchMax: 3,
		Logger:   quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	good := hashN(5)
	fakeSeeders(s, map[string]int32{good: 3})

	in := make(chan string, 3)
	in <- "zzzz"     // not hex
	in <- "deadbeef" // wrong length
	in <- good
	s.Run(context.Background(), in)
	defer s.Close()

	if got := recvOrFail(t, s.Out()); got != good {
		t.Fatalf("got %s want %s", got, good)
	}
	if st := s.Stats(); st.Scraped != 1 {
		t.Fatalf("Scraped = %d, want 1 (malformed skipped)", st.Scraped)
	}
}
