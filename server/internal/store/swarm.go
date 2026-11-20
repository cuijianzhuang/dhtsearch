package store

import (
	"context"
	"sort"
)

// SwarmStats is one row of swarm_stats: the latest tracker-scrape
// snapshot for an infohash. Seeders/Leechers/Completed are the
// per-field maximum across the trackers that answered the scrape
// round (see internal/scraper) — the industry convention, because
// trackers track disjoint swarms: a tracker that does not know the
// hash answers zero, and only the max keeps one tracker's ignorance
// from erasing another's knowledge. ScrapedAt is when that round
// ran (Unix seconds).
//
// ZeroSince/ZeroCount describe the current consecutive zero-seeder
// streak: ZeroSince is the scrape time of the first zero-seeder
// observation of the streak (0 when the last scrape saw seeders),
// ZeroCount how many consecutive scrapes have reported zero
// seeders. UpsertSwarmStats maintains both from the previous row;
// they are the evidence base for the dead-torrent health derivation
// below, never a verdict on their own.
type SwarmStats struct {
	InfoHash  string
	Seeders   int64
	Leechers  int64
	Completed int64
	ScrapedAt int64
	ZeroSince int64
	ZeroCount int64
}

// Health values derived from a swarm snapshot. The derivation is a
// probability judgement, never a binary claim: a torrent is only
// ever "likely" dead, because trackers can lose a swarm the DHT
// still carries, and a seeder can return after any scrape.
const (
	HealthUnknown    = "unknown"     // no scrape data at all
	HealthAlive      = "alive"       // latest scrape saw seeders
	HealthNoSeed     = "no_seed"     // no seeders, but leechers are still trying
	HealthSuspect    = "suspect"     // zero/zero, streak too short to judge
	HealthLikelyDead = "likely_dead" // zero/zero, long unbroken streak
)

// likely_dead thresholds. Three consecutive zero scrapes: the
// scraper re-checks popular hashes often, so a single zero is
// usually a tracker gap; three in a row from several independent
// trackers is evidence. 48 hours of unbroken zero: swarm deaths are
// announced by absence, and two days absorbs timezone/weekday
// cycles of casual seeders before the label sticks. Both must hold.
const (
	deadZeroStreakMin = 3
	deadZeroSpanSec   = 48 * 3600
)

// SwarmHealth derives the health label from a snapshot at read
// time; it is never stored. nil (no row) is HealthUnknown — unknown
// must stay distinguishable from a scraped true zero, which yields
// HealthSuspect (or worse) instead.
func SwarmHealth(s *SwarmStats) string {
	if s == nil {
		return HealthUnknown
	}
	if s.Seeders > 0 {
		return HealthAlive
	}
	if s.Leechers > 0 {
		return HealthNoSeed
	}
	if s.ZeroCount >= deadZeroStreakMin && s.ZeroSince > 0 &&
		s.ScrapedAt-s.ZeroSince >= deadZeroSpanSec {
		return HealthLikelyDead
	}
	return HealthSuspect
}

// upsertSwarm is UpsertSwarmStats' statement. The streak columns
// are maintained from the existing row inside the statement, so
// concurrent scrape workers cannot interleave a read-modify-write:
//   - new seeders > 0: the streak ends (zero_since 0, zero_count 0)
//   - new seeders = 0 while a streak is running (zero_since > 0):
//     keep the streak's start, count one more zero
//   - new seeders = 0 with no streak running: a streak starts at
//     this scrape (zero_since = scraped_at, zero_count = 1)
//
// The WHERE guard drops stale writes: scrape batches finish out of
// order, and an older round landing after a newer one must not
// rewind the snapshot or the streak. Column references in the SET
// expressions read the pre-update row on both backends.
func (d dialect) upsertSwarm() string {
	return `INSERT INTO swarm_stats (info_hash, seeders, leechers, completed, scraped_at, zero_since, zero_count)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(info_hash) DO UPDATE SET
			seeders = excluded.seeders,
			leechers = excluded.leechers,
			completed = excluded.completed,
			scraped_at = excluded.scraped_at,
			zero_since = CASE
				WHEN excluded.seeders > 0 THEN 0
				WHEN swarm_stats.zero_since > 0 THEN swarm_stats.zero_since
				ELSE excluded.scraped_at END,
			zero_count = CASE
				WHEN excluded.seeders > 0 THEN 0
				WHEN swarm_stats.zero_since > 0 THEN swarm_stats.zero_count + 1
				ELSE 1 END
		WHERE excluded.scraped_at >= swarm_stats.scraped_at`
}

// UpsertSwarmStats records one scrape round's snapshots. Rows are
// written verbatim (the caller aggregates across trackers); the
// streak columns of the incoming values are ignored — the statement
// derives them — except on a fresh insert, where they seed the
// streak, so callers pass them zeroed and this function fills them
// in. A batch runs in one transaction: at the production rate of a
// few dozen scrapes a minute, per-batch transactions are cheap and
// keep a crash from leaving half a round behind.
func (s *Store) UpsertSwarmStats(ctx context.Context, rows []SwarmStats) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(s.d.rebind(s.d.upsertSwarm()))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		zeroSince, zeroCount := int64(0), int64(0)
		if r.Seeders == 0 {
			zeroSince, zeroCount = r.ScrapedAt, 1
		}
		if _, err := stmt.ExecContext(ctx, r.InfoHash, r.Seeders, r.Leechers,
			r.Completed, r.ScrapedAt, zeroSince, zeroCount); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// swarmBatch bounds one IN (...) lookup; SearchEx may ask for a
// whole filtered match set when sorting by seeders.
const swarmBatch = 500

// SwarmStatsFor returns the snapshots for hashes that have one,
// keyed by info hash. Hashes without a row are absent from the map
// — that absence is the "unknown" signal, distinct from a stored
// zero.
func (s *Store) SwarmStatsFor(ctx context.Context, hashes []string) (map[string]SwarmStats, error) {
	out := make(map[string]SwarmStats, len(hashes))
	for start := 0; start < len(hashes); start += swarmBatch {
		end := start + swarmBatch
		if end > len(hashes) {
			end = len(hashes)
		}
		chunk := hashes[start:end]
		args := make([]interface{}, len(chunk))
		for i, h := range chunk {
			args[i] = h
		}
		rows, err := s.queryContext(ctx,
			`SELECT info_hash, seeders, leechers, completed, scraped_at, zero_since, zero_count
			 FROM swarm_stats WHERE info_hash IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var sw SwarmStats
			if err := rows.Scan(&sw.InfoHash, &sw.Seeders, &sw.Leechers,
				&sw.Completed, &sw.ScrapedAt, &sw.ZeroSince, &sw.ZeroCount); err != nil {
				rows.Close()
				return nil, err
			}
			out[sw.InfoHash] = sw
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// attachSwarm fills each item's Swarm from the snapshots table.
func (s *Store) attachSwarm(ctx context.Context, items []Torrent) error {
	if len(items) == 0 {
		return nil
	}
	hashes := make([]string, len(items))
	for i := range items {
		hashes[i] = items[i].InfoHash
	}
	m, err := s.SwarmStatsFor(ctx, hashes)
	if err != nil {
		return err
	}
	attachSwarmFrom(items, m)
	return nil
}

// attachSwarmFrom fills Swarm fields from an already-loaded map
// (SearchEx loads the full match set's snapshots when sorting by
// seeders or hiding the likely-dead, and reuses them for the page).
func attachSwarmFrom(items []Torrent, m map[string]SwarmStats) {
	for i := range items {
		if sw, ok := m[items[i].InfoHash]; ok {
			cp := sw
			items[i].Swarm = &cp
		}
	}
}

// sortBySeeders orders hits by scraped seeder count, descending.
// Hits with no snapshot sort below every scraped one — including a
// scraped true zero: "nobody reported on it" is weaker than "a
// tracker reported nobody seeding". Ties fall back to created_at
// (descending) and then info_hash (ascending), the same total-order
// convention as sortByRelevance, so pages are stable and both
// backends agree.
func sortBySeeders(hits []Torrent, swarm map[string]SwarmStats) {
	seeders := func(t Torrent) int64 {
		if sw, ok := swarm[t.InfoHash]; ok {
			return sw.Seeders
		}
		return -1
	}
	sort.SliceStable(hits, func(i, j int) bool {
		si, sj := seeders(hits[i]), seeders(hits[j])
		if si != sj {
			return si > sj
		}
		if hits[i].CreatedAt != hits[j].CreatedAt {
			return hits[i].CreatedAt > hits[j].CreatedAt
		}
		return hits[i].InfoHash < hits[j].InfoHash
	})
}
