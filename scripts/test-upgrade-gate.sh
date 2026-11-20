#!/usr/bin/env bash
# test-upgrade-gate.sh — hook-mode tests for the upgrade version gate
# in install.sh: when the target version is the one already installed,
# `upgrade` must be a no-op (no download, no backup, no restart), and
# a downgrade target must be refused. No root, no network, nothing
# written outside a temp DHTSEARCH_ROOT — curl is stubbed on PATH:
# the GitHub API lookup answers a fixture "latest" release, and any
# other request (i.e. a package download) is recorded and failed.
#
#   ./scripts/test-upgrade-gate.sh
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
MARKER="$ROOT/downloads-attempted"

STUB_BIN="$ROOT/stub-bin"
mkdir -p "$STUB_BIN"
cat > "$STUB_BIN/curl" <<'EOF'
#!/bin/sh
# Test stub for curl. The GitHub "latest release" lookup is answered
# from the STUB_LATEST fixture; anything else is a package download:
# record it in STUB_MARKER and fail, so a flow that reaches the
# download step is both visible and stopped there.
for arg in "$@"; do
    case "$arg" in
        *api.github.com*)
            printf '{"tag_name": "%s"}\n' "${STUB_LATEST:-}"
            exit 0
            ;;
    esac
done
echo "download attempted: $*" >> "${STUB_MARKER:?}"
exit 22
EOF
# git stub: fail instantly so a --from-source run that gets past the
# gate dies at the clone step instead of touching the network.
cat > "$STUB_BIN/git" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod +x "$STUB_BIN/curl" "$STUB_BIN/git"

# seed_install <version> — a minimal installed tree under the root.
seed_install() {
    rm -rf "${ROOT:?}/var" "${ROOT:?}/usr"
    mkdir -p "$DATA_DIR_TEST" "$(dirname "$BIN_TEST")"
    printf 'dummy binary\n' > "$BIN_TEST"
    printf '%s\n' "$1" > "$DATA_DIR_TEST/VERSION"
    rm -f "$MARKER"
}

# run_install <args...> — install.sh in hook mode with stubbed curl;
# STUB_LATEST is passed through from the caller's environment.
run_install() {
    PATH="$STUB_BIN:$PATH" STUB_MARKER="$MARKER" \
        DHTSEARCH_ROOT="$ROOT" DHTSEARCH_SKIP_SYSTEMD=1 \
        bash "$INSTALL" "$@"
}

no_backups() {
    # no_backups <what> — the gate must not have created any snapshot.
    if [ -d "$DATA_DIR_TEST/backups" ] \
        && [ -n "$(find "$DATA_DIR_TEST/backups" -name '*.bak' -print -quit)" ]; then
        fail "$1: a backup snapshot was created"
    fi
}

# --- 1. equal version (latest): full no-op ---------------------------
seed_install v0.1.14
out="$(STUB_LATEST=v0.1.14 run_install upgrade)" || fail "equal-version upgrade exit code"
contains "$out" "已是最新版本 v0.1.14，无需升级" "equal-version skip message"
[ ! -e "$MARKER" ] || fail "equal-version: a download was attempted"
no_backups "equal-version"

# --- 2. equal version via --version: same no-op ----------------------
seed_install v0.1.14
out="$(run_install upgrade --version v0.1.14)" || fail "--version equal exit code"
contains "$out" "已是最新版本 v0.1.14，无需升级" "--version equal skip message"
[ ! -e "$MARKER" ] || fail "--version equal: a download was attempted"
no_backups "--version equal"

# --- 3. equal version via --file: version parsed from the tarball ----
seed_install v0.1.14
PKG_DIR="$ROOT/pkg-src"
mkdir -p "$PKG_DIR/dhtsearch-v0.1.14-linux-amd64"
printf 'dummy\n' > "$PKG_DIR/dhtsearch-v0.1.14-linux-amd64/dhtsearch-server"
tar -czf "$ROOT/dhtsearch-v0.1.14-linux-amd64.tar.gz" -C "$PKG_DIR" dhtsearch-v0.1.14-linux-amd64
out="$(run_install upgrade --file "$ROOT/dhtsearch-v0.1.14-linux-amd64.tar.gz")" \
    || fail "--file equal exit code"
contains "$out" "已是最新版本 v0.1.14，无需升级" "--file equal skip message"
[ ! -e "$MARKER" ] || fail "--file equal: a download was attempted"
no_backups "--file equal"

# --- 4. newer version: proceeds past the gate to the download --------
seed_install v0.1.14
rc=0
out="$(STUB_LATEST=v0.2.0 run_install upgrade --dry-run)" || rc=$?
[ "$rc" -ne 0 ] || fail "newer-version: run should die at the stubbed download"
contains "$out" "升级：v0.1.14 -> v0.2.0" "newer-version gate proceed line"
contains "$out" "Downloading dhtsearch-v0.2.0" "newer-version reaches the download"
not_contains "$out" "无需升级" "newer-version is not skipped"
[ -e "$MARKER" ] || fail "newer-version: no download was attempted"

# --- 5. numeric ordering: v0.1.9 -> v0.1.10 is an upgrade ------------
seed_install v0.1.9
rc=0
out="$(run_install upgrade --dry-run --version v0.1.10)" || rc=$?
[ "$rc" -ne 0 ] || fail "v0.1.10 over v0.1.9 should proceed to the stubbed download"
contains "$out" "Downloading dhtsearch-v0.1.10" "v0.1.9 -> v0.1.10 proceeds"
not_contains "$out" "低于当前" "v0.1.9 -> v0.1.10 is not a downgrade"

# --- 6. downgrade refused ---------------------------------------------
seed_install v0.1.14
rc=0
out="$(run_install upgrade --version v0.1.9 2>&1)" || rc=$?
[ "$rc" -ne 0 ] || fail "downgrade: exit code must be non-zero"
contains "$out" "目标版本 v0.1.9 低于当前版本 v0.1.14" "downgrade refusal message"
[ ! -e "$MARKER" ] || fail "downgrade: a download was attempted"
no_backups "downgrade"

# --- 7. --from-source bypasses the gate -------------------------------
seed_install v0.1.14
rc=0
out="$(run_install upgrade --from-source 2>&1)" || rc=$?
contains "$out" "继续执行升级流程" "--from-source bypass note"
not_contains "$out" "无需升级" "--from-source is not skipped"

# --- 8. panel upgrade-request with nothing to do: completed no-op ----
seed_install v0.1.14
cat > "$DATA_DIR_TEST/update-request" <<'EOF'
REQUEST_ID=test-req-1
REQUESTED_AT=2026-11-20 15:00:00
REQUESTED_BY=admin
EOF
out="$(STUB_LATEST=v0.1.14 run_install upgrade-request)" || fail "panel no-op exit code"
[ -f "$DATA_DIR_TEST/update-status" ] || fail "panel no-op: update-status not written"
status_content="$(cat "$DATA_DIR_TEST/update-status")"
contains "$status_content" "STATE=succeeded" "panel no-op status state"
contains "$status_content" "REQUEST_ID=test-req-1" "panel no-op status request id"
contains "$status_content" "已是最新版本 v0.1.14，无需升级" "panel no-op status message"
[ ! -e "$DATA_DIR_TEST/update-request" ] || fail "panel no-op: request file not consumed"
[ ! -e "$MARKER" ] || fail "panel no-op: a download was attempted"
no_backups "panel no-op"

echo "upgrade-gate tests: all passed"
