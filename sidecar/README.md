# dhtsearch-lt-sidecar

The **libtorrent metadata-fetch foundation** for DHTSearch
(`META_FETCHER=libtorrent`), packaged as a separate process so the
Go server stays a pure-Go static binary — no cgo, no change to the
one-click install or the cross-compiled release pipeline.

The Go supervisor (`server/internal/metadata/libtorrent`) spawns
this binary, speaks framed JSON over its stdin/stdout
([PROTOCOL.md](PROTOCOL.md)), restarts it with backoff when it dies,
and fails server startup loudly when it is missing or cannot start:
selecting the foundation is an explicit operator choice and is never
silently downgraded.

Inside is one libtorrent-rasterbar 2.x session configured for
metadata-only crawling: every torrent in `upload_mode` (no piece
requests ever), removed the moment its metadata lands. Peer supply
is the magnet's tracker list + PEX, the session's own DHT
(get_peers; LSD/UPnP/NAT-PMP stay off) and the per-fetch peer hints
the Go side forwards from its peer-hint cache — the same supplies
the anacrolix fetcher combines. The session listens on OS-assigned
ports, so its DHT can never collide with the crawler's DHT ports.

## Building

CI builds it (`.github/workflows/sidecar.yml`, bookworm containers,
static libtorrent from the pinned tag in `build.sh`) and attaches
`dhtsearch-lt-sidecar-linux-<arch>` (+ `.sha256`) to GitHub releases
as an **optional** asset; `scripts/install.sh` downloads and places
it next to `dhtsearch-server` when present.

Locally, with Docker:

```sh
docker build -t dhtsearch-lt-sidecar sidecar/
docker run --rm -v "$PWD/sidecar/dist:/out" dhtsearch-lt-sidecar \
    cp /src/sidecar/dist/dhtsearch-lt-sidecar /out/
```

Or natively on Debian bookworm with the packages from the
Dockerfile installed: `./build.sh`.

The Dockerfile is two-stage: a `libtorrent` stage that compiles
only libtorrent (its sole repo input is `build.sh`, which carries
the version pin and cmake flags) and a `build` stage that copies
that install prefix and compiles just the sidecar. CI caches the
stages with buildx/GHA layer caching, so the libtorrent compile
only redoes when `build.sh` or the apt toolchain changes.
`build.sh` accepts `SIDECAR_BUILD_MODE=all|libtorrent|sidecar`
(default `all`) to run one half; the Dockerfile stages use it.
