#!/usr/bin/env bash
# package-release.sh — build a self-contained DHTSearch release tarball.
#
# Produces, in the output directory:
#   dhtsearch-<version>-linux-<arch>.tar.gz
#   dhtsearch-<version>-linux-<arch>.tar.gz.sha256
#
# The tarball contains everything scripts/install.sh needs on a target
# machine (no Go / Node toolchain required there, only a Node.js runtime
# for the Next.js standalone server):
#
#   dhtsearch-<version>-linux-<arch>/
#     dhtsearch-server   Go API/crawler binary (static, CGO off)
#     web/               Next.js standalone build (server.js, .next, public)
#     env.example        full configuration reference
#     install.sh         copy of scripts/install.sh
#     LICENSE, README.md
#
# Used by .github/workflows/release.yml and for local verification:
#   ./scripts/package-release.sh --version v0.1.0 --arch amd64
#
# Environment:
#   NEXT_PUBLIC_API_BASE   baked into the web build (the web server fetches
#                          the API server-side). Default http://127.0.0.1:8080,
#                          matching install.sh's default HTTP_ADDR.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION=""
ARCH=""
OUT_DIR="$ROOT_DIR/dist"
SKIP_WEB=0

usage() {
    cat <<'EOF'
Usage: scripts/package-release.sh [--version vX.Y.Z] [--arch amd64|arm64]
                                  [--out DIR] [--skip-web]

  --version   release version used in the tarball name (default: dev-<short sha>)
  --arch      target linux architecture: amd64 (default) or arm64
  --out       output directory (default: ./dist)
  --skip-web  reuse an existing web/.next build instead of rebuilding
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --version) VERSION="$2"; shift 2 ;;
        --arch) ARCH="$2"; shift 2 ;;
        --out) OUT_DIR="$2"; shift 2 ;;
        --skip-web) SKIP_WEB=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done

if [ -z "$ARCH" ]; then
    case "$(uname -m)" in
        x86_64) ARCH=amd64 ;;
        aarch64|arm64) ARCH=arm64 ;;
        *) echo "unsupported host arch: $(uname -m)" >&2; exit 1 ;;
    esac
fi
case "$ARCH" in
    amd64|arm64) ;;
    *) echo "--arch must be amd64 or arm64" >&2; exit 2 ;;
esac

if [ -z "$VERSION" ]; then
    VERSION="dev-$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)"
fi

PKG_NAME="dhtsearch-${VERSION}-linux-${ARCH}"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT
STAGE_DIR="$WORK_DIR/$PKG_NAME"
mkdir -p "$STAGE_DIR" "$OUT_DIR"

echo "==> Building server (linux/$ARCH)"
(cd "$ROOT_DIR/server" && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w" -o "$STAGE_DIR/dhtsearch-server" ./cmd/server)

if [ "$SKIP_WEB" -eq 0 ]; then
    echo "==> Building web (Next.js standalone)"
    (cd "$ROOT_DIR/web" && npm ci && \
        NEXT_PUBLIC_API_BASE="${NEXT_PUBLIC_API_BASE:-http://127.0.0.1:8080}" npm run build)
else
    echo "==> Reusing existing web/.next build (--skip-web)"
fi

echo "==> Assembling web standalone"
mkdir -p "$STAGE_DIR/web"
cp -R "$ROOT_DIR/web/.next/standalone/." "$STAGE_DIR/web/"
mkdir -p "$STAGE_DIR/web/.next"
cp -R "$ROOT_DIR/web/.next/static" "$STAGE_DIR/web/.next/static"
if [ -d "$ROOT_DIR/web/public" ]; then
    cp -R "$ROOT_DIR/web/public" "$STAGE_DIR/web/public"
fi
test -f "$STAGE_DIR/web/server.js" || { echo "web/server.js missing in standalone build" >&2; exit 1; }
# The standalone server refuses to start without its .next build output
# ("Could not find a production build in the './.next' directory"). A
# package missing it installs fine but the web service crash-loops, so
# treat it as a build failure here, never as a shippable package.
for f in .next/BUILD_ID .next/required-server-files.json; do
    test -f "$STAGE_DIR/web/$f" || { echo "release package would be broken: web/$f missing from standalone build" >&2; exit 1; }
done

# Optional: bundle the libtorrent sidecar when a local build exists
# (sidecar/dist, see sidecar/build.sh). Release CI ships the sidecar
# as a separate per-arch asset instead, and install.sh fetches it
# alongside the package; bundling here only serves source builds.
if [ -f "$ROOT_DIR/sidecar/dist/dhtsearch-lt-sidecar" ]; then
    cp "$ROOT_DIR/sidecar/dist/dhtsearch-lt-sidecar" "$STAGE_DIR/dhtsearch-lt-sidecar"
    chmod 755 "$STAGE_DIR/dhtsearch-lt-sidecar"
    echo "Bundled libtorrent sidecar from sidecar/dist"
fi
cp "$ROOT_DIR/env.example" "$STAGE_DIR/env.example"
cp "$ROOT_DIR/scripts/install.sh" "$STAGE_DIR/install.sh"
cp "$ROOT_DIR/LICENSE" "$STAGE_DIR/LICENSE"
cp "$ROOT_DIR/README.md" "$STAGE_DIR/README.md"
chmod 755 "$STAGE_DIR/dhtsearch-server" "$STAGE_DIR/install.sh"

echo "==> Creating $PKG_NAME.tar.gz"
tar -czf "$OUT_DIR/$PKG_NAME.tar.gz" -C "$WORK_DIR" "$PKG_NAME"
(cd "$OUT_DIR" && sha256sum "$PKG_NAME.tar.gz" > "$PKG_NAME.tar.gz.sha256")
echo "==> Done: $OUT_DIR/$PKG_NAME.tar.gz"
cat "$OUT_DIR/$PKG_NAME.tar.gz.sha256"
