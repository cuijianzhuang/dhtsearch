package metadata

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anacrolix/torrent"
)

// hashWithFirstByte builds a 40-char hex infohash whose first byte is b.
func hashWithFirstByte(b byte) string {
	return fmt.Sprintf("%02x%s", b, strings.Repeat("0", 38))
}

// Routing must be a pure function of the hash: the same hash always
// lands on the same shard client, or per-client AddTorrentSpec dedup
// stops meaning anything and one swarm's torrents straddle two locks.
func TestShardIndexDeterministic(t *testing.T) {
	h := "97a29535b9e2f0e46a1b09a4b0c0e4f0a1b2c3d4"
	for i := 0; i < 100; i++ {
		if got := shardIndex(h, 8); got != shardIndex(h, 8) {
			t.Fatalf("shardIndex(%q) not deterministic: %d", h, got)
		}
	}
	// Hex case must not change the route: hashes reach the fetcher in
	// both cases, and they decode to the same first byte.
	if shardIndex(strings.ToUpper(h), 8) != shardIndex(h, 8) {
		t.Fatal("uppercase and lowercase forms of one hash routed to different shards")
	}
}

// First-byte routing over the full byte range must hit every shard
// and spread evenly: 256 first bytes over 8 shards is exactly 32
// each. A skewed route would recreate the single-lock hot spot on
// whichever shard the skew favours.
func TestShardIndexBalanced(t *testing.T) {
	const shards = 8
	counts := make([]int, shards)
	for b := 0; b < 256; b++ {
		idx := shardIndex(hashWithFirstByte(byte(b)), shards)
		if idx < 0 || idx >= shards {
			t.Fatalf("shardIndex out of range: %d", idx)
		}
		counts[idx]++
	}
	for i, n := range counts {
		if n != 256/shards {
			t.Fatalf("shard %d got %d of 256 first bytes, want %d", i, n, 256/shards)
		}
	}
}

func TestShardIndexEdgeCases(t *testing.T) {
	h := hashWithFirstByte(0xff)
	if got := shardIndex(h, 1); got != 0 {
		t.Fatalf("one shard: got %d, want 0", got)
	}
	if got := shardIndex(h, 0); got != 0 {
		t.Fatalf("zero shards: got %d, want 0", got)
	}
	if got := shardIndex("a", 8); got != 0 {
		t.Fatalf("too short: got %d, want 0", got)
	}
	if got := shardIndex("zz"+strings.Repeat("0", 38), 8); got != 0 {
		t.Fatalf("non-hex: got %d, want 0", got)
	}
}

// Every shard client must be built from metadataClientConfig, and
// that config is where the peer-source decisions live: periodic DHT
// announce off (PR #30 — it was the lock wedge's fuel) and no DHT
// servers at all (with the announcer off, nothing consumes them; the
// fetcher's peers come from magnet trackers and PEX).
func TestMetadataClientConfigPeerSources(t *testing.T) {
	cfg := metadataClientConfig()
	if cfg.PeriodicallyAnnounceTorrentsToDht {
		t.Fatal("PeriodicallyAnnounceTorrentsToDht = true, want false on every shard")
	}
	if !cfg.NoDHT {
		t.Fatal("NoDHT = false, want true on every shard")
	}
}

// NewFetcher must build the configured number of shard clients,
// default and clamp as documented, and clientFor must route stably
// onto all of them.
func TestNewFetcherShards(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want int
	}{
		{"default", Config{Workers: 128}, DefaultShards},
		{"explicit", Config{Workers: 128, Shards: 4}, 4},
		{"single", Config{Workers: 128, Shards: 1}, 1},
		{"clamped to workers", Config{Workers: 4, Shards: 64}, 4},
		{"workers default too", Config{Shards: 3}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := NewFetcher(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if len(f.clients) != tc.want {
				t.Fatalf("built %d shard clients, want %d", len(f.clients), tc.want)
			}
			// Routing is stable and reaches every shard: first bytes
			// 0..want-1 map to distinct clients, repeatably.
			seen := make(map[*torrent.Client]bool)
			for b := 0; b < tc.want; b++ {
				h := hashWithFirstByte(byte(b))
				c := f.clientFor(h)
				if c != f.clientFor(h) {
					t.Fatalf("clientFor(%q) not stable", h)
				}
				seen[c] = true
			}
			if len(seen) != tc.want {
				t.Fatalf("routing reached %d distinct clients, want %d", len(seen), tc.want)
			}
		})
	}
}
