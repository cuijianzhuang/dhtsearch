#!/usr/bin/env bash
# test-upgrade-progress.sh — hook-mode tests for the upgrade progress
# markers (PROGRESS / PHASE) that do_upgrade records in the panel's
# update-status file while a panel-requested upgrade runs. No root, no
# network, nothing written outside a temp DHTSEARCH_ROOT: curl is
# stubbed on PATH (GitHub API answers a fixture "latest", the release
# tarball is a fixture package, its .sha256 is computed from it), and
# tar is wrapped so the stubs can snapshot update-status at real stage
# boundaries — the values observed must be the stages the script had
# actually reached at that moment, in increasing order.
#
#   ./scripts/test-upgrade-progress.sh
#
# Exits non-zero on the first failed assertion.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
INSTALL="scripts/install.sh"

fail() { echo "FAIL: $1" >&2; exit 1; }

contains() {
    # contains <output> <literal> <what>
    case "$1" in
        *"$2"*) ;;
        *) fail "$3: expected output to contain '$2'" ;;
    esac
}

not_contains() {
    # not_contains <output> <literal> <what>
    case "$1" in
        *"$2"*) fail "$3: expected output NOT to contain '$2'" ;;
    esac
}

ROOT="$(mktemp -d)"
trap 'rm -rf "$ROOT"' EXIT
DATA_DIR_TEST="$ROOT/var/lib/dhtsearch"
BIN_TEST="$ROOT/usr/local/bin/dhtsearch-server"
STATUS_TEST="$DATA_DIR_TEST/update-status"
SNAP_DIR="$ROOT/snaps"
mkdir -p "$SNAP_DIR"

# Fixture release package: the layout install_package_files requires.
PKG_DIR="$ROOT/pkg-src/dhtsearch-v0.2.0-linux-amd64"
mkdir -p "$PKG_DIR/web/.next"
printf 'new binary\n' > "$PKG_DIR/dhtsearch-server"
printf '// standalone\n' > "$PKG_DIR/web/server.js"
printf 'build123\n' > "$PKG_DIR/web/.next/BUILD_ID"
printf '{}\n' > "$PKG_DIR/web/.next/required-server-files.json"
cp "$INSTALL" "$PKG_DIR/install.sh"
FIXTURE="$ROOT/fixture.tar.gz"
tar -czf "$FIXTURE" -C "$ROOT/pkg-src" dhtsearch-v0.2.0-linux-amd64

REAL_TAR="$(command -v tar)"
STUB_BIN="$ROOT/stub-bin"
mkdir -p "$STUB_BIN"
cat > "$STUB_BIN/curl" <<'EOF'
#!/bin/sh
# Test stub for curl. The GitHub "latest release" lookup is answered
# from the STUB_LATEST fixture; the release tarball is the fixture
# package and its .sha256 is computed from it. The first API lookup
# and the tarball download snapshot the update-status file, so the
# test can assert which stage the upgrade had reached at that point.
# STUB_FAIL_DOWNLOAD=1 makes the tarball download fail instead.
out=""; url=""; prev=""
for arg in "$@"; do
    [ "$prev" = "-o" ] && out="$arg"
    case "$arg" in http*) url="$arg" ;; esac
    prev="$arg"
done
snap() {
    # snap <name> — keep only the first snapshot of each boundary.
    if [ -n "${STUB_SNAP_DIR:-}" ] && [ -f "${STUB_STATUS_FILE:-}" ] \
        && [ ! -e "$STUB_SNAP_DIR/$1" ]; then
        cp "$STUB_STATUS_FILE" "$STUB_SNAP_DIR/$1"
    fi
}
case "$url" in
    *api.github.com*)
        snap at-api-lookup
        printf '{"tag_name": "%s"}\n' "${STUB_LATEST:-}"
        exit 0
        ;;
    *.tar.gz.sha256)
        base="$(basename "${url%.sha256}")"
        hash="$(sha256sum "$STUB_FIXTURE" | cut -d' ' -f1)"
        printf '%s  %s\n' "$hash" "$base" > "$out"
        exit 0
        ;;
    *.tar.gz)
        snap at-download
        [ "${STUB_FAIL_DOWNLOAD:-0}" = "1" ] && exit 22
        cp "$STUB_FIXTURE" "$out"
        exit 0
        ;;
esac
exit 22
EOF
cat > "$STUB_BIN/tar" <<'EOF'
#!/bin/sh
# Test wrapper for tar: the first extraction in a panel upgrade is
# install_package_files unpacking the new package, which happens
# right after the backup stage — snapshot update-status there, then
# delegate to the real tar.
if [ -n "${STUB_SNAP_DIR:-}" ] && [ -f "${STUB_STATUS_FILE:-}" ] \
    && [ ! -e "$STUB_SNAP_DIR/at-extract" ]; then
    cp "$STUB_STATUS_FILE" "$STUB_SNAP_DIR/at-extract"
fi
exec "$STUB_REAL_TAR" "$@"
EOF
chmod +x "$STUB_BIN/curl" "$STUB_BIN/tar"

# seed_install <version> — a minimal installed tree under the root,
# plus a clean snapshot dir.
seed_install() {
    rm -rf "${ROOT:?}/var" "${ROOT:?}/usr" "$SNAP_DIR"
    mkdir -p "$DATA_DIR_TEST" "$(dirname "$BIN_TEST")" "$SNAP_DIR"
    printf 'dummy binary\n' > "$BIN_TEST"
    printf '%s\n' "$1" > "$DATA_DIR_TEST/VERSION"
}

# write_request <id> — a pending panel upgrade request.
write_request() {
    cat > "$DATA_DIR_TEST/update-request" <<EOF
REQUEST_ID=$1
REQUESTED_AT=2026-11-20 15:00:00
REQUESTED_BY=admin
EOF
}

# run_install <args...> — install.sh in hook mode with stubbed curl/tar;
# STUB_* knobs are passed through from the caller's environment.
run_install() {
    PATH="$STUB_BIN:$PATH" \
        STUB_FIXTURE="$FIXTURE" STUB_SNAP_DIR="$SNAP_DIR" \
        STUB_STATUS_FILE="$STATUS_TEST" STUB_REAL_TAR="$REAL_TAR" \
        DHTSEARCH_ROOT="$ROOT" DHTSEARCH_SKIP_SYSTEMD=1 \
        bash "$INSTALL" "$@"
}

# --- 1. panel-requested upgrade: progress climbs stage by stage ------
seed_install v0.1.14
write_request prog-req-1
out="$(STUB_LATEST=v0.2.0 run_install upgrade-request)" || fail "progress success exit code"
contains "$out" "Upgraded: v0.1.14 -> v0.2.0" "progress success log line"

snap="$(cat "$SNAP_DIR/at-api-lookup")" || fail "progress: no api-lookup snapshot"
contains "$snap" "STATE=running" "api-lookup snapshot state"
contains "$snap" "PROGRESS=2" "api-lookup snapshot progress"
contains "$snap" "PHASE=准备" "api-lookup snapshot phase"

snap="$(cat "$SNAP_DIR/at-download")" || fail "progress: no download snapshot"
contains "$snap" "PROGRESS=10" "download snapshot progress"
contains "$snap" "PHASE=下载更新" "download snapshot phase"

snap="$(cat "$SNAP_DIR/at-extract")" || fail "progress: no extract snapshot"
contains "$snap" "PROGRESS=50" "extract snapshot progress"
contains "$snap" "PHASE=备份完成" "extract snapshot phase"

status_content="$(cat "$STATUS_TEST")"
contains "$status_content" "STATE=succeeded" "progress final state"
contains "$status_content" "PROGRESS=100" "progress final value"
contains "$status_content" "PHASE=完成" "progress final phase"
contains "$status_content" "upgraded v0.1.14 -> v0.2.0" "progress final message"

# --- 2. download failure: progress freezes at the last stage ---------
seed_install v0.1.14
write_request prog-req-2
out="$(STUB_LATEST=v0.2.0 STUB_FAIL_DOWNLOAD=1 run_install upgrade-request)" \
    || fail "progress failure exit code"
status_content="$(cat "$STATUS_TEST")"
contains "$status_content" "STATE=failed" "failure final state"
contains "$status_content" "PROGRESS=10" "failure keeps last progress"
contains "$status_content" "PHASE=下载更新" "failure keeps last phase"
not_contains "$status_content" "PROGRESS=100" "failure never reaches 100"

# --- 3. panel no-op (already latest): no progress fields at all ------
seed_install v0.1.14
write_request prog-req-3
out="$(STUB_LATEST=v0.1.14 run_install upgrade-request)" || fail "progress no-op exit code"
status_content="$(cat "$STATUS_TEST")"
contains "$status_content" "STATE=succeeded" "no-op final state"
contains "$status_content" "已是最新版本 v0.1.14，无需升级" "no-op final message"
not_contains "$status_content" "PROGRESS=" "no-op carries no progress"

# --- 4. plain CLI upgrade never touches update-status ----------------
seed_install v0.1.14
printf 'STATE=failed\nREQUEST_ID=someone-elses\nPROGRESS=42\n' > "$STATUS_TEST"
before="$(cat "$STATUS_TEST")"
rc=0
out="$(STUB_LATEST=v0.2.0 STUB_FAIL_DOWNLOAD=1 run_install upgrade 2>&1)" || rc=$?
[ "$rc" -ne 0 ] || fail "cli upgrade should die at the stubbed download"
[ "$(cat "$STATUS_TEST")" = "$before" ] || fail "cli upgrade clobbered update-status"

echo "upgrade-progress tests: all passed"
