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
metadata-only crawling: DHT/LSD/UPnP/NAT-PMP off, every torrent in
`upload_mode` (no piece requests ever), removed the moment its
metadata lands. Peer supply is the magnet's tracker list + PEX —
identical inputs to the anacrolix fetcher.

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
    cp /src/dist/dhtsearch-lt-sidecar /out/
```

Or natively on Debian bookworm with the packages from the
Dockerfile installed: `./build.sh`.
