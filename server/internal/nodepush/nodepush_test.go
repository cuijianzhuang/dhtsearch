package nodepush

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/scraper"
)

// fakeCenter is an in-process stand-in for the center's node
// surface: it records what it receives and answers from canned
// admission state.
type fakeCenter struct {
	mu       sync.Mutex
	ingests  []IngestRequest
	stored   map[string]bool
	backoff  map[string]bool
	failMode bool // answer 500 to everything
}

func (f *fakeCenter) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/node/check", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failMode {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var req CheckRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		resp := CheckResponse{Stored: []string{}, Backoff: []string{}}
		for _, h := range req.Hashes {
			switch {
			case f.stored[h]:
				resp.Stored = append(resp.Stored, h)
			case f.backoff[h]:
				resp.Backoff = append(resp.Backoff, h)
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("POST /api/node/ingest", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failMode {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var req IngestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		f.ingests = append(f.ingests, req)
		_ = json.NewEncoder(w).Encode(IngestResponse{Ingested: len(req.Records), Swarm: len(req.Swarm)})
	})
	return mux
}

func newFakeCenter(t *testing.T) (*fakeCenter, *Client) {
	t.Helper()
	fc := &fakeCenter{stored: map[string]bool{}, backoff: map[string]bool{}}
	srv := httptest.NewServer(fc.handler())
	t.Cleanup(srv.Close)
	return fc, NewClient(srv.URL, "tok", "worker-1", nil)
}

func testLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func TestPusherDeliversRecordsAndSwarm(t *testing.T) {
	fc, client := newFakeCenter(t)
	p := NewPusher(client, testLogger())
	rec := metadata.Record{
		InfoHash: strings.Repeat("a", 40), Name: "A Fine Film", TotalSize: 1 << 30,
		FileCount: 1, Files: []filter.File{{Path: "a.mkv", Size: 1 << 30}},
		Source: metadata.SourceScrape,
	}
	p.Push(rec, "feed-alias")
	p.PushSwarm([]scraper.SwarmStat{{Hash: rec.InfoHash, Seeders: 5, Leechers: 2, Completed: 9}})
	p.flush(context.Background())

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.ingests) != 1 {
		t.Fatalf("center got %d ingest calls, want 1", len(fc.ingests))
	}
	got := fc.ingests[0]
	if got.Node != "worker-1" {
		t.Fatalf("ingest node = %q, want worker-1", got.Node)
	}
	if len(got.Records) != 1 {
		t.Fatalf("ingest records = %d, want 1", len(got.Records))
	}
	wire := got.Records[0]
	if wire.InfoHash != rec.InfoHash || wire.Name != rec.Name || wire.Source != "scrape" || wire.Alias != "feed-alias" {
		t.Fatalf("wire record = %+v", wire)
	}
	if len(wire.Files) != 1 || wire.Files[0].Path != "a.mkv" {
		t.Fatalf("wire files = %+v", wire.Files)
	}
	if len(got.Swarm) != 1 || got.Swarm[0].Seeders != 5 || got.Swarm[0].Completed != 9 {
		t.Fatalf("wire swarm = %+v", got.Swarm)
	}
	if p.Pushed() != 1 {
		t.Fatalf("Pushed = %d, want 1", p.Pushed())
	}
	if n, m := p.Buffered(); n != 0 || m != 0 {
		t.Fatalf("Buffered after flush = %d/%d, want 0/0", n, m)
	}
}

func TestPusherRetriesOnceThenRequeues(t *testing.T) {
	fc, client := newFakeCenter(t)
	p := NewPusher(client, testLogger())
	p.Push(metadata.Record{InfoHash: strings.Repeat("b", 40), Name: "x", TotalSize: 1}, "")

	fc.mu.Lock()
	fc.failMode = true
	fc.mu.Unlock()
	p.flush(context.Background())
	if n, _ := p.Buffered(); n != 1 {
		t.Fatalf("after failed flush: buffered = %d, want 1 (requeued)", n)
	}
	if p.Pushed() != 0 {
		t.Fatalf("Pushed after failure = %d, want 0", p.Pushed())
	}

	// Center recovers: the same record is delivered by the next flush.
	fc.mu.Lock()
	fc.failMode = false
	fc.mu.Unlock()
	p.flush(context.Background())
	if n, _ := p.Buffered(); n != 0 {
		t.Fatalf("after recovery flush: buffered = %d, want 0", n)
	}
	if p.Pushed() != 1 {
		t.Fatalf("Pushed after recovery = %d, want 1", p.Pushed())
	}
}

func TestPusherBufferDropsOldest(t *testing.T) {
	_, client := newFakeCenter(t)
	p := NewPusher(client, testLogger())
	p.bufferRecords = 3
	for i := 0; i < 5; i++ {
		p.Push(metadata.Record{InfoHash: strings.Repeat(string(rune('a'+i)), 40), Name: "x"}, "")
	}
	if n, _ := p.Buffered(); n != 3 {
		t.Fatalf("buffered = %d, want 3 (cap)", n)
	}
	if p.Dropped() != 2 {
		t.Fatalf("Dropped = %d, want 2", p.Dropped())
	}
	// The survivors are the NEWEST records.
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.records[0].rec.InfoHash != strings.Repeat("c", 40) {
		t.Fatalf("oldest survivor = %s, want c… (a and b dropped)", p.records[0].rec.InfoHash)
	}
}

func TestCheckerAdmissionDecisions(t *testing.T) {
	fc, client := newFakeCenter(t)
	storedHash := strings.Repeat("a", 40)
	backoffHash := strings.Repeat("b", 40)
	freshHash := strings.Repeat("c", 40)
	fc.stored[storedHash] = true
	fc.backoff[backoffHash] = true

	c := NewChecker(client, testLogger())
	items := []metadata.Item{
		{Hash: storedHash, Source: metadata.SourceDHT},
		{Hash: backoffHash, Source: metadata.SourceDHT},
		{Hash: backoffHash, Source: metadata.SourceScrape, Seeders: 4}, // fresh seeders exempt it
		{Hash: backoffHash, Source: metadata.SourceFeed},               // feed is exempt
		{Hash: freshHash, Source: metadata.SourceDHT},
	}
	admitted := c.AdmitBatch(items)
	if len(admitted) != 3 {
		t.Fatalf("admitted = %d, want 3 (%v)", len(admitted), admitted)
	}
	if admitted[0].Hash != backoffHash || admitted[1].Hash != backoffHash || admitted[2].Hash != freshHash {
		t.Fatalf("admitted hashes = %v", admitted)
	}
	stored, backoff := c.Skipped()
	if stored != 1 || backoff != 1 {
		t.Fatalf("Skipped = %d/%d, want 1/1", stored, backoff)
	}
}

func TestCheckerFailsOpen(t *testing.T) {
	fc, client := newFakeCenter(t)
	fc.mu.Lock()
	fc.failMode = true
	fc.mu.Unlock()
	c := NewChecker(client, testLogger())
	items := []metadata.Item{
		{Hash: strings.Repeat("a", 40), Source: metadata.SourceDHT},
		{Hash: strings.Repeat("b", 40), Source: metadata.SourceDHT},
	}
	if admitted := c.AdmitBatch(items); len(admitted) != 2 {
		t.Fatalf("fail-open admitted = %d, want 2", len(admitted))
	}
	if stored, backoff := c.Skipped(); stored != 0 || backoff != 0 {
		t.Fatalf("Skipped after failure = %d/%d, want 0/0", stored, backoff)
	}
}

func TestCheckerDisabledAdmitsAll(t *testing.T) {
	fc, client := newFakeCenter(t)
	fc.stored[strings.Repeat("a", 40)] = true
	c := NewChecker(client, testLogger())
	c.EnabledFunc = func() bool { return false }
	items := []metadata.Item{{Hash: strings.Repeat("a", 40), Source: metadata.SourceDHT}}
	if admitted := c.AdmitBatch(items); len(admitted) != 1 {
		t.Fatalf("disabled checker admitted = %d, want 1", len(admitted))
	}
}

func TestGateChannelFilters(t *testing.T) {
	fc, client := newFakeCenter(t)
	fc.stored[strings.Repeat("a", 40)] = true
	c := NewChecker(client, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan metadata.Item, 3)
	in <- metadata.Item{Hash: strings.Repeat("a", 40), Source: metadata.SourceDHT}
	in <- metadata.Item{Hash: strings.Repeat("b", 40), Source: metadata.SourceDHT}
	in <- metadata.Item{Hash: strings.Repeat("c", 40), Source: metadata.SourceScrape}
	close(in)
	var got []string
	timeout := time.After(5 * time.Second)
	out := c.GateChannel(ctx, in)
	for {
		select {
		case it, ok := <-out:
			if !ok {
				if len(got) != 2 || got[0] != strings.Repeat("b", 40) || got[1] != strings.Repeat("c", 40) {
					t.Fatalf("gated = %v, want [b… c…]", got)
				}
				return
			}
			got = append(got, it.Hash)
		case <-timeout:
			t.Fatal("gate channel did not drain")
		}
	}
}

func TestRecordWireRoundTrip(t *testing.T) {
	rec := metadata.Record{
		InfoHash: strings.Repeat("a", 40), Name: "n", TotalSize: 42, FileCount: 2,
		Files:  []filter.File{{Path: "x", Size: 40}, {Path: "y", Size: 2}},
		Source: metadata.SourceFeed,
	}
	back := RecordWireFrom(rec, "alias").Record()
	if !reflect.DeepEqual(back, rec) {
		t.Fatalf("round trip = %+v, want %+v", back, rec)
	}
	// An unknown source label folds to dht.
	w := RecordWireFrom(rec, "")
	w.Source = "carrier-pigeon"
	if got := w.Record().Source; got != metadata.SourceDHT {
		t.Fatalf("unknown source folded to %v, want dht", got)
	}
}
