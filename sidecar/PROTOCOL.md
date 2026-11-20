# Sidecar IPC protocol

Normative description of the wire between the Go server
(`server/internal/metadata/libtorrent`) and the libtorrent sidecar
(`sidecar/src/main.cpp`). The Go half of this document is
`protocol.go` in that package; if the two disagree, this file wins
and the disagreement is a bug.

## Transport

The sidecar's **stdin/stdout**. The Go supervisor spawns the process,
owns both pipes, and forwards the sidecar's stderr into the server
log (prefix `lt-sidecar:`). No sockets, no files: process death
closes the pipes, which both sides treat as a terminal event.

Every message is one **frame**: a 4-byte big-endian unsigned length
followed by that many bytes of UTF-8 JSON. Frames are capped at
4 MiB; a length outside `1..4MiB` desynchronises the stream and the
reader aborts the process generation.

## Messages

### server → sidecar

| type       | fields                                                       | meaning |
|------------|--------------------------------------------------------------|---------|
| `fetch`    | `id`, `magnet`, `deadline_ms`, `max_files`, `peer_hints` (optional) | start a metadata fetch job |
| `cancel`   | `id`                                                         | drop the job (no result will follow) |
| `shutdown` | —                                                            | exit cleanly |

`magnet` is constructed by the Go side exactly as the anacrolix
fetcher constructs it: `magnet:?xt=urn:btih:<hex>` plus the shared
`&tr=` tracker suffix. `deadline_ms` is the job's total budget (the
META_TIMEOUT value); the sidecar removes the torrent and answers
`timeout` when it expires. `max_files` caps the `files` array in the
result; `file_count` always reports the true count.

`peer_hints` is an optional array of `"host:port"` strings
(`"1.2.3.4:6881"`, `"[2001:db8::1]:6881"` — the shapes Go's
`netip.AddrPort.String()` produces): initial peers for this hash
from the Go side's peer-hint cache (`metadata.Item.Peers`), which
the sidecar connects (`connect_peer`) alongside its tracker, PEX
and DHT supply. The field is additive: a sidecar that predates it
ignores it, a server that predates it never sends it, and a
malformed hint is dropped by the sidecar without failing the job.

### sidecar → server

| type     | fields                                                        | meaning |
|----------|---------------------------------------------------------------|---------|
| `ready`  | `sidecar`, `libtorrent` (version strings)                     | sent once, right after the session is up; the supervisor's readiness handshake |
| `peers`  | `id`, `peers`                                                 | peer count for a job: first time it exceeds 0, then on change (~100 ms poll cadence) |
| `status` | `dht_running`, `dht_nodes`                                    | the session's own DHT state, sent on change (~1 s poll cadence): whether its DHT is running and its routing-table node count |
| `result` | `id`, `outcome`, …                                            | terminal job outcome (below) |

Result outcomes:

| outcome   | extra fields                                        | Go classification |
|-----------|-----------------------------------------------------|-------------------|
| `ok`      | `name`, `total_size`, `file_count`, `files[]`       | `fetched` |
| `timeout` | `peers` (last known count)                          | `meta_timed_out` |
| `error`   | `error` (human-readable)                            | `meta_failed` |

`files[]` entries are `{"path": "...", "size": N}` with paths
normalised to the anacrolix fetcher's `Record` shape: for multi-file
torrents the torrent root name is stripped from the first component;
for single-file torrents the path is the torrent name.

## Who classifies what

The Go side owns the outcome counters and the prescreen decision,
mirroring `metadata.Fetcher`:

- The sidecar reports `peers` events and enforces the deadline.
- If the prescreen window (META_PRESCREEN_TIMEOUT, inside the total
  budget) closes with no `peers > 0` event and no result, the Go
  side sends `cancel` and counts `prescreen_dropped`.
- If the Go-side deadline fires first (sidecar wedged), the Go side
  cancels and counts `meta_timed_out`.
- If the process dies, every in-flight job gets a terminal transport
  error and counts `meta_failed`; the supervisor restarts the
  process with backoff (1 s doubling to 30 s, reset after 60 s of
  stable life).

A result arriving for an unknown or already-classified job id is
dropped by the receiver. Job ids are monotonically increasing per
supervisor lifetime and never reused across process generations.

## Lifecycle

1. Supervisor spawns the binary (no required arguments;
   `--max-active N` caps concurrent torrents, `--save-path DIR`
   overrides the scratch directory, `--version` prints versions and
   exits).
2. Sidecar builds its session and sends `ready`. Until then, job
   submissions wait (bounded, 3 s) and then fail as transport errors.
3. Fetcher construction blocks until the first `ready` (bounded,
   ~45 s across restarts) so a `META_FETCHER=libtorrent` server with
   a broken sidecar fails startup loudly instead of degrading.
4. On shutdown the supervisor sends `shutdown`, waits 2 s, then
   kills. Stdin EOF (parent death) also ends the sidecar, so it can
   never be orphaned.
