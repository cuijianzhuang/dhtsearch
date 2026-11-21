package store

import (
	"context"
	"reflect"
	"testing"
)

// Selection fixture times: a fixed "now" and a 48h min-age, so
// the cutoff sits 48 hours back.
const (
	rsNow    = int64(1_800_000_000)
	rsCutoff = rsNow - 48*3600
)

// assertStalestSelection plants the selection fixture and checks
// StalestSwarmHashes against it. Shared by the SQLite test and
// the Postgres twin: the selection statement is plain SQL over
// torrents LEFT JOIN swarm_stats, and both dialects must agree on
// membership AND order.
func assertStalestSelection(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()

	neverA, neverB := swarmHash('a'), swarmHash('b')
	oldSnap, midSnap := swarmHash('c'), swarmHash('d')
	freshSnap, boundarySnap := swarmHash('e'), swarmHash('f')

	plant := func(hash string, scrapedAt *int64) {
		seedTorrentAt(t, s, hash, rsNow-100*24*3600)
		if scrapedAt != nil {
			seedSwarmRow(t, s, SwarmStats{
				InfoHash: hash, Seeders: 3, Leechers: 1, Completed: 9,
				ScrapedAt: *scrapedAt,
			})
		}
	}
	at := func(ts int64) *int64 { return &ts }
	plant(neverA, nil)
	plant(neverB, nil)
	plant(oldSnap, at(rsNow-100*3600))
	plant(midSnap, at(rsNow-60*3600))
	plant(freshSnap, at(rsNow-10*3600)) // younger than the min age: gated out
	plant(boundarySnap, at(rsCutoff))   // exactly at the cutoff: not older, gated out

	// Never-scraped first (they count as scraped at the epoch,
	// ties on info_hash), then stalest snapshot first.
	want := []string{neverA, neverB, oldSnap, midSnap}
	got, err := s.StalestSwarmHashes(ctx, rsCutoff, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selection = %v, want %v", got, want)
	}

	// The limit trims from the fresh end of the same order.
	got, err = s.StalestSwarmHashes(ctx, rsCutoff, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want[:2]) {
		t.Fatalf("limited selection = %v, want %v", got, want[:2])
	}

	// A cutoff at the epoch leaves only the never-scraped: every
	// real snapshot is newer than it.
	got, err = s.StalestSwarmHashes(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want[:2]) {
		t.Fatalf("epoch-cutoff selection = %v, want %v", got, want[:2])
	}
}

func TestStalestSwarmHashes(t *testing.T) {
	s := openTest(t)
	assertStalestSelection(t, s)
}
