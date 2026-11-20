#!/usr/bin/env bash
# build.sh — build the libtorrent sidecar and a static libtorrent to
# link it against. Runs inside Debian bookworm (see Dockerfile /
# .github/workflows/sidecar.yml); the resulting binary dynamically
# links only glibc (>= 2.36, the bookworm floor) so it runs on
# Debian 12 and 13 hosts of the same architecture.
#
# Prerequisites (apt): g++ cmake git pkg-config libboost-all-dev
# libssl-dev nlohmann-json3-dev. The script does NOT run apt itself;
# the Dockerfile and the CI workflow own the package list so it stays
# in one place per environment.
#
# Output: sidecar/dist/dhtsearch-lt-sidecar
set -euo pipefail

LIBTORRENT_VERSION="${LIBTORRENT_VERSION:-v2.0.11}"
SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="${SIDECAR_BUILD_DIR:-$SRC_DIR/.build}"
PREFIX="${SIDECAR_PREFIX:-/usr/local}"
JOBS="${JOBS:-$(nproc)}"

mkdir -p "$WORK"

if [ ! -d "$WORK/libtorrent" ]; then
    echo "==> Fetching libtorrent $LIBTORRENT_VERSION"
    # --recurse-submodules is load-bearing: libtorrent's CMakeLists
    # compiles deps/try_signal (a submodule) unconditionally, and a
    # plain clone leaves it empty — CMake then fails at generate time
    # with "Cannot find source file: deps/try_signal/try_signal.cpp".
    git clone --depth 1 --recurse-submodules --shallow-submodules \
        --branch "$LIBTORRENT_VERSION" \
        https://github.com/arvidn/libtorrent.git "$WORK/libtorrent"
fi

echo "==> Building libtorrent (static)"
# deprecated-functions=OFF makes libtorrent define TORRENT_NO_DEPRECATE,
# which selects ABI version 3 (the libtorrent::v2 symbol namespace);
# the sidecar's CMakeLists pins the matching TORRENT_ABI_VERSION=3
# consumer define. Keep the two in step.
cmake -S "$WORK/libtorrent" -B "$WORK/libtorrent-build" \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX="$PREFIX" \
    -DBUILD_SHARED_LIBS=OFF \
    -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
    -Ddeprecated-functions=OFF
cmake --build "$WORK/libtorrent-build" -j "$JOBS"
cmake --install "$WORK/libtorrent-build"

echo "==> Building dhtsearch-lt-sidecar"
cmake -S "$SRC_DIR" -B "$WORK/sidecar-build" \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_PREFIX_PATH="$PREFIX"
cmake --build "$WORK/sidecar-build" -j "$JOBS"

mkdir -p "$SRC_DIR/dist"
cp "$WORK/sidecar-build/dhtsearch-lt-sidecar" "$SRC_DIR/dist/dhtsearch-lt-sidecar"
echo "==> Done: $SRC_DIR/dist/dhtsearch-lt-sidecar"
"$SRC_DIR/dist/dhtsearch-lt-sidecar" --version
ldd "$SRC_DIR/dist/dhtsearch-lt-sidecar" || true
