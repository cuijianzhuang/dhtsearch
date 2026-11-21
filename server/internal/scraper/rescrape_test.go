package scraper

import (
	"context"
	"encoding/hex"
	"errors"
	"sync/atomic"
	"testing"
)

func newTestRescraper(t *testing.T, trackers ...string) *Rescraper {
	t.Helper()
	r, err := NewRescraper(Config{Trackers: trackers, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

func statsByHash(stats []SwarmStat) map[string]SwarmStat {
	m := make(map[string]SwarmStat, len(stats))
	for _, sw := range stats {
		m[sw.Hash] = sw
	}
	return m
}

// TestRescraperMergesAndCovers: a re-scrape round merges trackers
// per-field-max exactly like ingest, a covered all-zero hash is
// present with zeros, and a hash no tracker answered for is
// absent — the coverage contract the swarm write path relies on.
func TestRescraperMergesAndCovers(t *testing.T) {
	r := newTestRescraper(t,
		"udp://a.invalid:1111/announce", "udp://b.invalid:2222/announce")
	h1, h2, h3 := hashN(1), hashN(2), hashN(3)
	r.scrapeTracker = func(_ context.Context, tc *trackerConn, ihs [][20]byte) ([]SwarmSample, error) {
		switch tc.host {
		case "a.invalid:1111":
			// Answers for h1 and h2 only: the short response
			// covers just its prefix, so h3 stays uncovered here.
			return []SwarmSample{
				{Seeders: 5, Leechers: 1, Completed: 10},
				{Seeders: 0, Leechers: 0, Completed: 0},
			}, nil
		default: // b.invalid:2222
			out := make([]SwarmSample, len(ihs))
			for i, ih := range ihs {
				if hex.EncodeToString(ih[:]) == h1 {
					out[i] = SwarmSample{Seeders: 9, Leechers: 7, Completed: 4}
				}
			}
			return out, nil
		}
	}
	got := statsByHash(r.Scrape(context.Background(), []string{h1, h2, h3}))
	if len(got) != 3 {
		t.Fatalf("got %d stats, want 3: %v", len(got), got)
	}
	// Per-field max across the two trackers, field by field.
	if sw := got[h1]; sw.Seeders != 9 || sw.Leechers != 7 || sw.Completed != 10 {
		t.Fatalf("h1 = %+v, want per-field max {9 7 10}", sw)
	}
	// Covered all-zero: present, with zeros.
	if sw, ok := got[h2]; !ok || sw.Seeders != 0 || sw.Leechers != 0 {
		t.Fatalf("h2 = %+v (present %v), want a present all-zero", sw, ok)
	}
	// h3 was covered by tracker B's full-length answer (zeros).
	if _, ok := got[h3]; !ok {
		t.Fatalf("h3 absent, want present (covered by B)")
	}
}

// TestRescraperUncoveredAbsent: when the only tracker answers a
// short prefix, the tail hashes are absent from the round — never
// invented zeros.
func TestRescraperUncoveredAbsent(t *testing.T) {
	r := newTestRescraper(t, "udp://a.invalid:1111/announce")
	h1, h2 := hashN(1), hashN(2)
	r.scrapeTracker = func(_ context.Context, _ *trackerConn, _ [][20]byte) ([]SwarmSample, error) {
		return []SwarmSample{{Seeders: 3}}, nil // covers h1 only
	}
	got := statsByHash(r.Scrape(context.Background(), []string{h1, h2}))
	if len(got) != 1 {
		t.Fatalf("got %d stats, want 1: %v", len(got), got)
	}
	if _, ok := got[h2]; ok {
		t.Fatalf("h2 present, want absent (no tracker answered for it)")
	}
}

// TestRescraperAllTrackersFail: a round where every tracker
// errors returns nothing at all — no data is never written as 0.
func TestRescraperAllTrackersFail(t *testing.T) {
	r := newTestRescraper(t, "udp://a.invalid:1111/announce")
	r.scrapeTracker = func(_ context.Context, _ *trackerConn, _ [][20]byte) ([]SwarmSample, error) {
		return nil, errors.New("tracker unreachable")
	}
	if got := r.Scrape(context.Background(), []string{hashN(1), hashN(2)}); len(got) != 0 {
		t.Fatalf("got %d stats from a failed round, want 0", len(got))
	}
}

// TestRescraperBatches: selections larger than BatchMax are split
// into batches and every hash comes back.
func TestRescraperBatches(t *testing.T) {
	r, err := NewRescraper(Config{
		Trackers: []string{"udp://a.invalid:1111/announce"},
		BatchMax: 2,
		Logger:   quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var calls atomic.Int64
	r.scrapeTracker = func(_ context.Context, _ *trackerConn, ihs [][20]byte) ([]SwarmSample, error) {
		calls.Add(1)
		out := make([]SwarmSample, len(ihs))
		for i := range out {
			out[i].Seeders = 1
		}
		return out, nil
	}
	hashes := []string{hashN(1), hashN(2), hashN(3), hashN(4), hashN(5), "not-a-hash"}
	got := r.Scrape(context.Background(), hashes)
	if len(got) != 5 {
		t.Fatalf("got %d stats, want 5 (invalid hash skipped)", len(got))
	}
	if calls.Load() != 3 {
		t.Fatalf("tracker calls = %d, want 3 batches of <=2", calls.Load())
	}
}
