package metadata

import (
	"testing"
	"time"
)

// The seeder count decides how long a fetch is worth waiting for. Cutting the
// timeout uniformly is known to destroy throughput here — the successful
// fetches are the slow ones — so the budget may only shrink for hashes the
// trackers say nobody is seeding, and must be left alone when nothing was
// measured.
func TestTimeoutForScalesWithSeeders(t *testing.T) {
	f := &Fetcher{cfg: Config{Timeout: 45 * time.Second}}
	for _, tc := range []struct {
		name    string
		seeders int32
		want    time.Duration
	}{
		{"never scraped", SeedersUnknown, 45 * time.Second},
		{"no seeders", 0, 15 * time.Second},
		{"thin swarm", 1, 30 * time.Second},
		{"thin swarm upper edge", thinSeeders - 1, 30 * time.Second},
		{"healthy swarm", thinSeeders, 60 * time.Second},
		{"popular", 5000, 60 * time.Second},
	} {
		if got := f.timeoutFor(tc.seeders); got != tc.want {
			t.Errorf("%s (seeders=%d): timeout = %v, want %v", tc.name, tc.seeders, got, tc.want)
		}
	}
}

// An unscraped feed must never be penalised: with the scraper off every
// request carries SeedersUnknown, and shortening those would reproduce the
// uniform-cut regression.
func TestTimeoutForUnknownMatchesConfigured(t *testing.T) {
	for _, d := range []time.Duration{10 * time.Second, 45 * time.Second, 2 * time.Minute} {
		f := &Fetcher{cfg: Config{Timeout: d}}
		if got := f.timeoutFor(SeedersUnknown); got != d {
			t.Errorf("timeout %v: got %v, want it unchanged", d, got)
		}
	}
}
