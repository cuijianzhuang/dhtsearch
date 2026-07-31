package main

import (
	"testing"

	"dhtsearch/server/internal/filter"
)

// The adult switch decides only the adult signal. Spam and undersized
// torrents are junk whatever the content policy is, so they must keep being
// dropped — a regression that gated them behind the same flag would fill the
// index with fake stubs and keyword-stuffed garbage.
func TestDropReason(t *testing.T) {
	for _, tc := range []struct {
		name        string
		res         filter.Result
		filterAdult bool
		wantStat    string
		wantDrop    bool
	}{
		{"clean torrent, filtering on", filter.Result{}, true, "", false},
		{"clean torrent, filtering off", filter.Result{}, false, "", false},

		{"adult, filtering on", filter.Result{Adult: true}, true, "adult_filtered", true},
		{"adult, filtering off", filter.Result{Adult: true}, false, "", false},

		{"spam is dropped regardless", filter.Result{Spam: true}, false, "spam_filtered", true},
		{"undersized is dropped regardless", filter.Result{TooSmall: true}, false, "size_filtered", true},

		// An adult torrent that is also spam stays out even with the switch
		// off: the switch admits adult content, not junk that happens to be
		// adult.
		{"adult+spam, filtering off", filter.Result{Adult: true, Spam: true}, false, "spam_filtered", true},
		{"adult+undersized, filtering off", filter.Result{Adult: true, TooSmall: true}, false, "size_filtered", true},

		// With filtering on, adult wins the attribution so the counter keeps
		// meaning "rejected for being adult".
		{"adult+spam, filtering on", filter.Result{Adult: true, Spam: true}, true, "adult_filtered", true},
	} {
		stat, drop := dropReason(tc.res, tc.filterAdult)
		if drop != tc.wantDrop || stat != tc.wantStat {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", tc.name, stat, drop, tc.wantStat, tc.wantDrop)
		}
	}
}
