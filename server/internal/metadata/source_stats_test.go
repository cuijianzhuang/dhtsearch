package metadata

import "testing"

// TestSourceTrackerMixedBatch: a mixed batch of attempts and
// outcomes across all three sources aggregates per source, and the
// per-source outcome sums reconcile with the attempts.
func TestSourceTrackerMixedBatch(t *testing.T) {
	var tr SourceTracker
	record := func(src Source, outcome FetchOutcome) {
		tr.Attempt(src)
		tr.Outcome(src, outcome)
	}
	// dht: 4 attempts — 1 fetched, 1 timed out, 1 failed, 1 prescreened.
	record(SourceDHT, OutcomeFetched)
	record(SourceDHT, OutcomeTimedOut)
	record(SourceDHT, OutcomeFailed)
	record(SourceDHT, OutcomePrescreened)
	// scrape: 3 attempts — 2 fetched, 1 prescreened.
	record(SourceScrape, OutcomeFetched)
	record(SourceScrape, OutcomeFetched)
	record(SourceScrape, OutcomePrescreened)
	// feed: 2 attempts — 2 fetched.
	record(SourceFeed, OutcomeFetched)
	record(SourceFeed, OutcomeFetched)
	// One attempt still in flight: counted, no outcome yet.
	tr.Attempt(SourceDHT)

	stats := tr.Stats()
	if len(stats) != 3 {
		t.Fatalf("Stats returned %d rows, want 3", len(stats))
	}
	want := []FetchSourceStats{
		{Source: SourceDHT, Attempts: 5, Fetched: 1, TimedOut: 1, Failed: 1, PrescreenDropped: 1},
		{Source: SourceScrape, Attempts: 3, Fetched: 2, PrescreenDropped: 1},
		{Source: SourceFeed, Attempts: 2, Fetched: 2},
	}
	for i, w := range want {
		if stats[i] != w {
			t.Fatalf("row %d = %+v, want %+v", i, stats[i], w)
		}
	}
}

// TestSourceTrackerUnknownSourceDefaultsToDHT: an out-of-range tag
// must land on the dht row, not corrupt the table or panic.
func TestSourceTrackerUnknownSourceDefaultsToDHT(t *testing.T) {
	var tr SourceTracker
	tr.Attempt(Source(99))
	tr.Outcome(Source(99), OutcomeFailed)
	tr.Attempt(Source(-1))
	stats := tr.Stats()
	if stats[0].Source != SourceDHT || stats[0].Attempts != 2 || stats[0].Failed != 1 {
		t.Fatalf("dht row = %+v, want attempts 2 failed 1", stats[0])
	}
	if stats[1].Attempts != 0 || stats[2].Attempts != 0 {
		t.Fatalf("other rows moved: %+v", stats)
	}
}

// TestSourceNamesAndStatKeys pins the published labels and the
// stats-table key format the API reads back.
func TestSourceNamesAndStatKeys(t *testing.T) {
	if SourceDHT.Name() != "dht" || SourceScrape.Name() != "scrape" || SourceFeed.Name() != "feed" {
		t.Fatalf("names = %q/%q/%q", SourceDHT.Name(), SourceScrape.Name(), SourceFeed.Name())
	}
	if Source(42).Name() != "dht" {
		t.Fatalf("unknown source names as %q, want dht", Source(42).Name())
	}
	cases := map[string]string{
		SourceStatKey(SourceDHT, SourceCounterAttempts):          "fetch_dht_attempts",
		SourceStatKey(SourceDHT, SourceCounterFetched):           "fetch_dht_fetched",
		SourceStatKey(SourceScrape, SourceCounterTimedOut):       "fetch_scrape_timed_out",
		SourceStatKey(SourceFeed, SourceCounterFailed):           "fetch_feed_failed",
		SourceStatKey(SourceFeed, SourceCounterPrescreenDropped): "fetch_feed_prescreen_dropped",
		SourceStatKey(Source(42), SourceCounterAttempts):         "fetch_dht_attempts",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("key = %q, want %q", got, want)
		}
	}
	if got := AllSources(); len(got) != 3 || got[0] != SourceDHT || got[1] != SourceScrape || got[2] != SourceFeed {
		t.Fatalf("AllSources = %v", got)
	}
}
