# Build the libtorrent sidecar in Debian bookworm (glibc 2.36 — the
# floor shared by Debian 12/13 hosts). Extract the binary with:
#
#   docker build -t dhtsearch-lt-sidecar sidecar/
#   docker run --rm -v "$PWD/sidecar/dist:/out" dhtsearch-lt-sidecar \
#       cp /src/sidecar/dist/dhtsearch-lt-sidecar /out/
#
# Two stages, split so CI layer caching can reuse the expensive one:
#
#   libtorrent — compiles ONLY libtorrent from source (~10-25 min).
#     Its only repo input is build.sh, which carries the version pin
#     (LIBTORRENT_VERSION) and the cmake flags, so this stage's layers
#     are invalidated exactly when a libtorrent build input changes
#     (build.sh, or the apt toolchain below) — edits to the sidecar's
#     own sources do NOT bust it. Do not widen the COPY in this stage.
#   build — copies the installed libtorrent prefix from stage 1 and
#     compiles just the sidecar (seconds). This is the final image,
#     and the extraction commands above work against it unchanged.
#
# CI (.github/workflows/sidecar.yml) builds this same Dockerfile via
# buildx with the GitHub Actions layer cache (type=gha, mode=max),
# so a warm run reuses stage 1 wholesale and only redoes stage 2.

FROM debian:bookworm AS libtorrent
RUN apt-get update && apt-get install -y --no-install-recommends \
        g++ cmake make git pkg-config ca-certificates \
        libboost-all-dev libssl-dev nlohmann-json3-dev \
    && rm -rf /var/lib/apt/lists/*
# ONLY build.sh: it is this stage's entire repo input (the libtorrent
# version pin and build flags live there). Copying anything else
# would let unrelated sidecar edits bust the cached libtorrent layer.
COPY build.sh /src/sidecar/build.sh
WORKDIR /src/sidecar
RUN SIDECAR_BUILD_MODE=libtorrent ./build.sh

FROM debian:bookworm AS build
# Keep this apt set identical to the libtorrent stage's: the two
# stages share one toolchain definition, and the apt set is part of
# the libtorrent layer's cache identity.
RUN apt-get update && apt-get install -y --no-install-recommends \
        g++ cmake make git pkg-config ca-certificates \
        libboost-all-dev libssl-dev nlohmann-json3-dev \
    && rm -rf /var/lib/apt/lists/*
COPY --from=libtorrent /usr/local /usr/local
COPY . /src/sidecar
WORKDIR /src/sidecar
RUN SIDECAR_BUILD_MODE=sidecar ./build.sh
