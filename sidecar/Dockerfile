# Build the libtorrent sidecar in Debian bookworm (glibc 2.36 — the
# floor shared by Debian 12/13 hosts). Extract the binary with:
#
#   docker build -t dhtsearch-lt-sidecar sidecar/
#   docker run --rm -v "$PWD/sidecar/dist:/out" dhtsearch-lt-sidecar \
#       cp /src/dist/dhtsearch-lt-sidecar /out/
#
# CI (.github/workflows/sidecar.yml) runs the same steps in a
# bookworm container job instead of Docker-in-Docker.
FROM debian:bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends \
        g++ cmake make git pkg-config ca-certificates \
        libboost-all-dev libssl-dev nlohmann-json3-dev \
    && rm -rf /var/lib/apt/lists/*
COPY . /src/sidecar
WORKDIR /src/sidecar
RUN ./build.sh
