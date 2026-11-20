#!/usr/bin/env bash
# test-restart-request.sh — hook-mode tests for the panel-requested
# service restart: the panel writes restart-request into the data
# dir, the per-minute upgrade agent (the same `dhtsearch
# upgrade-request` tick that handles upgrade requests) consumes it
# exactly once, "restarts" the API service (systemd is skipped in
# hook mode, so svc() only logs the systemctl call) and records the
# outcome in restart-status for the panel to display.
#
#   ./scripts/test-restart-request.sh
#
# No root, no systemd, nothing written outside a temp DHTSEARCH_ROOT.
# curl is stubbed only so the combined restart+upgrade scenario can
# answer the upgrade path's GitHub lookup with "already latest".
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
RESTART_REQ_TEST="$DATA_DIR_TEST/restart-request"
RESTART_STATUS_TEST="$DATA_DIR_TEST/restart-status"
UPDATE_STATUS_TEST="$DATA_DIR_TEST/update-status"

STUB_BIN="$ROOT/stub-bin"
mkdir -p "$STUB_BIN"
cat > "$STUB_BIN/curl" <<'EOF'
#!/bin/sh
# Only the upgrade path's GitHub "latest release" lookup is answered
# (with the STUB_LATEST fixture); anything else fails like a 404.
url=""
for arg in "$@"; do
    case "$arg" in http*) url="$arg" ;; esac
done
case "$url" in
    *api.github.com*)
        printf '{"tag_name": "%s"}\n' "${STUB_LATEST:-}"
        exit 0
        ;;
esac
exit 22
EOF
chmod +x "$STUB_BIN/curl"

# seed_install <version> — a minimal installed tree under the root.
seed_install() {
    rm -rf "${ROOT:?}/var" "${ROOT:?}/usr"
    mkdir -p "$DATA_DIR_TEST" "$(dirname "$BIN_TEST")"
    printf 'dummy binary\n' > "$BIN_TEST"
    printf '%s\n' "$1" > "$DATA_DIR_TEST/VERSION"
}

# write_restart_request <id> — a pending panel restart request.
write_restart_request() {
    cat > "$RESTART_REQ_TEST" <<EOF
REQUEST_ID=$1
REQUESTED_AT=2026-11-20 15:00:00
REQUESTED_BY=admin
EOF
}

# run_install <args...> — install.sh in hook mode.
run_install() {
    PATH="$STUB_BIN:$PATH" \
        DHTSEARCH_ROOT="$ROOT" DHTSEARCH_SKIP_SYSTEMD=1 \
        bash "$INSTALL" "$@"
}

# --- 1. a restart request is consumed and recorded -------------------
seed_install v0.1.14
write_restart_request restart-req-1
out="$(run_install upgrade-request)" || fail "restart: exit code"
contains "$out" "restart-request: restarting dhtsearch.service" "restart: run log"
contains "$out" "(systemd skipped) systemctl restart dhtsearch.service" "restart: only the API unit restarts"
not_contains "$out" "systemctl restart dhtsearch-web.service" "restart: web unit untouched"
[ ! -e "$RESTART_REQ_TEST" ] || fail "restart: request flag not consumed"
status_content="$(cat "$RESTART_STATUS_TEST")"
contains "$status_content" "STATE=succeeded" "restart: final state"
contains "$status_content" "REQUEST_ID=restart-req-1" "restart: status id"
contains "$status_content" "REQUESTED_BY=admin" "restart: status requester"
contains "$status_content" "FINISHED_AT=20" "restart: finished stamp"

# --- 2. an already-handled request id is dropped, not re-run ---------
seed_install v0.1.14
write_restart_request restart-req-2
cat > "$RESTART_STATUS_TEST" <<'EOF'
STATE=succeeded
REQUEST_ID=restart-req-2
REQUESTED_AT=2026-11-20 15:00:00
REQUESTED_BY=admin
STARTED_AT=2026-11-20 15:00:03
FINISHED_AT=2026-11-20 15:00:09
MESSAGE=service restarted; API is answering again
EOF
before="$(cat "$RESTART_STATUS_TEST")"
out="$(run_install upgrade-request)" || fail "dedupe: exit code"
not_contains "$out" "restart-request: restarting" "dedupe: no second restart"
[ ! -e "$RESTART_REQ_TEST" ] || fail "dedupe: request flag not dropped"
[ "$(cat "$RESTART_STATUS_TEST")" = "$before" ] || fail "dedupe: status rewritten"

# --- 3. no request: the tick leaves no restart trace ------------------
seed_install v0.1.14
out="$(run_install upgrade-request)" || fail "idle: exit code"
not_contains "$out" "restart-request:" "idle: no restart work"
[ ! -e "$RESTART_STATUS_TEST" ] || fail "idle: restart-status created"

# --- 4. restart and upgrade pending together: both are consumed -----
seed_install v0.1.14
write_restart_request restart-req-4
cat > "$DATA_DIR_TEST/update-request" <<'EOF'
REQUEST_ID=upgrade-req-4
REQUESTED_AT=2026-11-20 15:00:00
REQUESTED_BY=admin
EOF
out="$(STUB_LATEST=v0.1.14 run_install upgrade-request)" || fail "both: exit code"
contains "$out" "restart-request: restarting dhtsearch.service" "both: restart ran"
[ ! -e "$RESTART_REQ_TEST" ] || fail "both: restart flag not consumed"
contains "$(cat "$RESTART_STATUS_TEST")" "STATE=succeeded" "both: restart outcome"
contains "$(cat "$UPDATE_STATUS_TEST")" "STATE=succeeded" "both: upgrade outcome"

echo "restart-request tests: all passed"
