package store

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"dhtsearch/server/internal/filter"
)

// Retention fixture times: a fixed "now" so ages and streak spans
// are exact. minAge is 30 days, so the cutoff sits 30 days back.
const (
	retNow    = int64(1_800_000_000)
	retMinAge = int64(30 * 24 * 3600)
	retCutoff = retNow - retMinAge
	retOld    = retNow - 40*24*3600 // comfortably past the cutoff
	retYoung  = retNow - 10*24*3600 // comfortably short of it
)

// seedSwarmRow writes one swarm_stats row verbatim (the streak
// columns included), bypassing UpsertSwarmStats' streak machine so
// the fixture can place a hash in an exact health tier.
func seedSwarmRow(t *testing.T, s *Store, sw SwarmStats) {
	t.Helper()
	_, err := s.execContext(context.Background(),
		`INSERT INTO swarm_stats (info_hash, seeders, leechers, completed, scraped_at, zero_since, zero_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(info_hash) DO UPDATE SET seeders = excluded.seeders, leechers = excluded.leechers,
			completed = excluded.completed, scraped_at = excluded.scraped_at,
			zero_since = excluded.zero_since, zero_count = excluded.zero_count`,
		sw.InfoHash, sw.Seeders, sw.Leechers, sw.Completed, sw.ScrapedAt, sw.ZeroSince, sw.ZeroCount)
	if err != nil {
		t.Fatal(err)
	}
}

// deadSwarm is a snapshot squarely in the likely_dead tier: zero
// seeders and leechers, a 5-scrape zero streak spanning 72 hours.
func deadSwarm(hash string) SwarmStats {
	return SwarmStats{
		InfoHash: hash, Seeders: 0, Leechers: 0,
		ScrapedAt: retNow - 3600, ZeroSince: retNow - 3600 - 72*3600, ZeroCount: 5,
	}
}

func seedTorrentAt(t *testing.T, s *Store, hash string, createdAt int64) {
	t.Helper()
	err := s.Upsert(Torrent{
		InfoHash: hash, Name: "fixture " + hash[:4], TotalSize: 1 << 30, FileCount: 1,
		Files:     []filter.File{{Path: "movie.mkv", Size: 1 << 30}},
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// seedRetentionFixture plants one torrent per health tier and age
// combination. Eligible under retCutoff: deadOld, deadOldBlocked,
// deadBoundary — nothing else.
func seedRetentionFixture(t *testing.T, s *Store) (eligible, survivors []string) {
	t.Helper()
	ctx := context.Background()

	plant := func(hash string, createdAt int64, sw *SwarmStats) {
		seedTorrentAt(t, s, hash, createdAt)
		if sw != nil {
			sw.InfoHash = hash
			seedSwarmRow(t, s, *sw)
		}
	}

	deadOld := swarmHash('a')
	plant(deadOld, retOld, ptrSwarm(deadSwarm(deadOld)))
	// Its fetch backoff row must be deleted with it.
	if _, err := s.RecordFetchFailure(ctx, deadOld, "timeout", time.Unix(retNow-3600, 0), 0); err != nil {
		t.Fatal(err)
	}

	// Eligible AND blacklisted: the torrent row goes, the blocked
	// row must survive. The blocked row is inserted directly —
	// Block() itself would delete the torrent, which is a different
	// story than a hash that was indexed and later blacklisted by
	// hand in the database.
	deadOldBlocked := swarmHash('b')
	plant(deadOldBlocked, retOld, ptrSwarm(deadSwarm(deadOldBlocked)))
	if _, err := s.execContext(ctx,
		`INSERT INTO blocked (info_hash, reason, name, created_at) VALUES (?, 'test', 'fixture', ?)`,
		deadOldBlocked, retOld); err != nil {
		t.Fatal(err)
	}

	// Boundary: exactly 3 zero scrapes, a span of exactly 48h, and
	// created one second before the cutoff — eligible on all three.
	deadBoundary := swarmHash('i')
	boundary := deadSwarm(deadBoundary)
	boundary.ZeroCount = 3
	boundary.ZeroSince = boundary.ScrapedAt - 48*3600
	plant(deadBoundary, retCutoff-1, &boundary)

	eligible = []string{deadOld, deadOldBlocked, deadBoundary}

	deadYoung := swarmHash('c') // likely_dead but too young
	plant(deadYoung, retYoung, ptrSwarm(deadSwarm(deadYoung)))

	aliveOld := swarmHash('d') // seeders present
	alive := deadSwarm(aliveOld)
	alive.Seeders, alive.ZeroSince, alive.ZeroCount = 7, 0, 0
	plant(aliveOld, retOld, &alive)

	noSeedOld := swarmHash('e') // no seeders, but leechers remain
	noSeed := deadSwarm(noSeedOld)
	noSeed.Leechers = 2
	plant(noSeedOld, retOld, &noSeed)

	suspectStreak := swarmHash('f') // zero streak too short
	streak := deadSwarm(suspectStreak)
	streak.ZeroCount = 2
	plant(suspectStreak, retOld, &streak)

	suspectSpan := swarmHash('g') // streak long enough, span too short
	span := deadSwarm(suspectSpan)
	span.ZeroCount = 9
	span.ZeroSince = span.ScrapedAt - 24*3600
	plant(suspectSpan, retOld, &span)

	unknownOld := swarmHash('h') // never scraped at all
	plant(unknownOld, retOld, nil)

	atCutoff := swarmHash('j') // likely_dead, created exactly AT the cutoff
	plant(atCutoff, retCutoff, ptrSwarm(deadSwarm(atCutoff)))

	survivors = []string{deadYoung, aliveOld, noSeedOld, suspectStreak, suspectSpan, unknownOld, atCutoff}
	return eligible, survivors
}

func ptrSwarm(sw SwarmStats) *SwarmStats { return &sw }

// assertRetentionBehavior runs the retention assertions against a
// store of either backend (shared by the SQLite test and the
// env-gated Postgres test, so both dialects are pinned to the same
// eligibility and deletion semantics).
func assertRetentionBehavior(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	eligible, survivors := seedRetentionFixture(t, s)
	sort.Strings(eligible)

	count, err := s.CountLikelyDead(ctx, retCutoff)
	if err != nil {
		t.Fatal(err)
	}
	if count != int64(len(eligible)) {
		t.Fatalf("CountLikelyDead = %d, want %d (only likely_dead + old enough)", count, len(eligible))
	}

	hashes, next, err := s.ScanLikelyDead(ctx, retCutoff, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Fatalf("scan cursor = %q, want exhausted", next)
	}
	sort.Strings(hashes)
	if fmt.Sprint(hashes) != fmt.Sprint(eligible) {
		t.Fatalf("ScanLikelyDead = %v, want %v", hashes, eligible)
	}

	deleted, err := s.DeleteRetentionBatch(ctx, hashes)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != int64(len(eligible)) {
		t.Fatalf("deleted = %d, want %d", deleted, len(eligible))
	}

	// Eligible torrents are gone, with their swarm snapshots and
	// fetch backoff rows.
	for _, h := range eligible {
		got, err := s.AdminGet(ctx, h)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("torrent %s survived cleanup", h)
		}
	}
	swarm, err := s.SwarmStatsFor(ctx, eligible)
	if err != nil {
		t.Fatal(err)
	}
	if len(swarm) != 0 {
		t.Fatalf("swarm rows survived cleanup: %v", swarm)
	}
	if _, found, err := s.FetchAttemptRow(ctx, eligible[0]); err != nil || found {
		t.Fatalf("fetch_attempts row survived cleanup (found=%v, err=%v)", found, err)
	}

	// The blocked row of the blacklisted eligible hash survives.
	blocked, err := s.IsBlocked(eligible[1])
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Fatalf("blocked row for %s was deleted by cleanup", eligible[1])
	}

	// Every other tier survives untouched.
	for _, h := range survivors {
		got, err := s.AdminGet(ctx, h)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatalf("torrent %s (%s tier) was wrongly cleaned", h, h[:1])
		}
	}
	if count, err := s.CountLikelyDead(ctx, retCutoff); err != nil || count != 0 {
		t.Fatalf("CountLikelyDead after cleanup = %d (err %v), want 0", count, err)
	}
}

func TestRetentionCleanup(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertRetentionBehavior(t, s)
}

// TestScanLikelyDeadCursor: the keyset walk drains the eligible
// set in small pages without repeats or losses, stepping over
// ineligible candidates whose hashes interleave the eligible ones.
func TestScanLikelyDeadCursor(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	var want []string
	for i := 0; i < 5; i++ {
		h := fmt.Sprintf("e%039d", 2*i) // even slots: eligible
		seedTorrentAt(t, s, h, retOld)
		seedSwarmRow(t, s, deadSwarm(h))
		want = append(want, h)
		bad := fmt.Sprintf("e%039d", 2*i+1) // odd slots: suspect, must be stepped over
		seedTorrentAt(t, s, bad, retOld)
		sw := deadSwarm(bad)
		sw.ZeroCount = 1
		seedSwarmRow(t, s, sw)
	}

	var got []string
	cursor := ""
	for i := 0; i < 10; i++ { // hard bound: the walk must terminate
		hashes, next, err := s.ScanLikelyDead(ctx, retCutoff, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, hashes...)
		if next == "" {
			break
		}
		cursor = next
		if i == 9 {
			t.Fatal("walk did not terminate")
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("walked %v, want %v", got, want)
	}
}

// TestSetStat: absolute writes land, overwrite, and coexist with
// the delta-written counters.
func TestSetStat(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.SetStatContext(ctx, "retention_last_deleted", 12); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatContext(ctx, "retention_last_deleted", 3); err != nil {
		t.Fatal(err)
	}
	if err := s.IncrStatContext(ctx, "retention_deleted", 5); err != nil {
		t.Fatal(err)
	}
	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats["retention_last_deleted"] != 3 || stats["retention_deleted"] != 5 {
		t.Fatalf("stats = %v", stats)
	}
}
