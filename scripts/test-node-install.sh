#!/usr/bin/env bash
# test-node-install.sh — hook-mode tests for the crawler-node
# installer flows in install.sh: installing as a worker node writes
# the three node keys and installs no web unit, the token is only
# ever shown masked, conversion and de-conversion via repeat
# install, upgrade preserving the node keys (and not reviving the
# web service), and the connectivity self-check's four outcomes.
# No root, no network, nothing written outside a temp
# DHTSEARCH_ROOT: installs run from fixture --file packages and
# curl is stubbed on PATH (the node-check outcome is driven by
# STUB_NODE_CODE; an unset code simulates a network failure).
#
#   ./scripts/test-node-install.sh
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

env_has() {
    # env_has <key> <value> <what>
    grep -qE "^$1=$2\$" "$ENV_TEST" || fail "$3: env file should carry $1=$2"
}

env_lacks() {
    # env_lacks <key> <what>
    if grep -qE "^$1=" "$ENV_TEST"; then
        fail "$2: env file should NOT carry $1"
    fi
}

ROOT="$(mktemp -d)"
trap 'rm -rf "$ROOT"' EXIT
ENV_TEST="$ROOT/etc/dhtsearch/dhtsearch.env"
UNIT_DIR_TEST="$ROOT/etc/systemd/system"
DATA_DIR_TEST="$ROOT/var/lib/dhtsearch"

# Fixture release packages (the layout install_package_files
# requires), two versions so upgrade has somewhere to go.
make_pkg() {
    local dir="$ROOT/pkg-src-$1/dhtsearch-$1-linux-amd64"
    mkdir -p "$dir/web/.next"
    printf 'binary %s\n' "$1" > "$dir/dhtsearch-server"
    printf '// standalone\n' > "$dir/web/server.js"
    printf 'build-%s\n' "$1" > "$dir/web/.next/BUILD_ID"
    printf '{}\n' > "$dir/web/.next/required-server-files.json"
    cp "$INSTALL" "$dir/install.sh"
    tar -czf "$ROOT/fixture-$1.tar.gz" -C "$ROOT/pkg-src-$1" "dhtsearch-$1-linux-amd64"
}
make_pkg v0.2.0
make_pkg v0.3.0

STUB_BIN="$ROOT/stub-bin"
mkdir -p "$STUB_BIN"
cat > "$STUB_BIN/curl" <<'EOF'
#!/bin/sh
# Test stub for curl. A /api/node/check call is answered with the
# STUB_NODE_CODE fixture as its HTTP status (node_check reads the
# -w '%{http_code}' output); an unset code exits 7 with no output,
# simulating a network failure. Everything else (health probes)
# succeeds quietly.
for arg in "$@"; do
    case "$arg" in
        */api/node/check*)
            if [ -n "${STUB_NODE_CODE:-}" ]; then
                printf '%s' "$STUB_NODE_CODE"
                exit 0
            fi
            exit 7
            ;;
    esac
done
exit 0
EOF
chmod +x "$STUB_BIN/curl"

# run_install <args...> — install.sh in hook mode with stubbed curl.
run_install() {
    PATH="$STUB_BIN:$PATH" DHTSEARCH_ROOT="$ROOT" DHTSEARCH_SKIP_SYSTEMD=1 \
        bash "$INSTALL" "$@"
}

CENTER="https://center.example:10800"
TOKEN="tok-secret-abcdef"

# --- 1. fresh install as a node ------------------------------------
out="$(STUB_NODE_CODE=200 run_install install --file "$ROOT/fixture-v0.2.0.tar.gz" \
    --node-center "$CENTER" --node-token "$TOKEN" --node-name hk-1)" \
    || fail "node install exit"
env_has CENTER_URL "$CENTER" "node install"
env_has NODE_TOKEN "$TOKEN" "node install"
env_has NODE_NAME "hk-1" "node install"
[ -f "$UNIT_DIR_TEST/dhtsearch.service" ] || fail "node install: API unit missing"
[ ! -f "$UNIT_DIR_TEST/dhtsearch-web.service" ] \
    || fail "node install: web unit must NOT be written in node mode"
contains "$out" "角色：爬虫节点（中心 $CENTER，节点名 hk-1）" "node install role line"
contains "$out" "连通性测试：通" "node install self-check (200)"
contains "$out" "tok-…" "node install masked token"
not_contains "$out" "$TOKEN" "node token never printed in full"

# status reports the node role without any network access.
out="$(run_install status)" || fail "status exit (node)"
contains "$out" "角色：   爬虫节点（中心 $CENTER，节点名 hk-1）" "status role line"
contains "$out" "Health:  Web not run (crawler node mode)" "status web line in node mode"

# --- 2. upgrade preserves the node keys, web stays off -------------
out="$(run_install upgrade --file "$ROOT/fixture-v0.3.0.tar.gz")" \
    || fail "node upgrade exit"
env_has CENTER_URL "$CENTER" "node upgrade"
env_has NODE_TOKEN "$TOKEN" "node upgrade"
env_has NODE_NAME "hk-1" "node upgrade"
[ ! -f "$UNIT_DIR_TEST/dhtsearch-web.service" ] \
    || fail "node upgrade: web unit must not reappear"
# The enable/disable calls are silenced even in hook mode, but the
# restart line is not: a node restarts the API unit alone.
contains "$out" "systemctl restart dhtsearch.service" "node upgrade restarts the API"
not_contains "$out" "systemctl restart dhtsearch.service dhtsearch-web.service" \
    "node upgrade does not restart the web service"
grep -qx 'v0.3.0' "$DATA_DIR_TEST/VERSION" || fail "node upgrade: VERSION not updated"

# --- 3. self-check outcomes -----------------------------------------
# The check runs automatically at the end of a node install; a
# repeat install against the same root re-runs it with each stubbed
# outcome. A failed check must never fail the install.
out="$(STUB_NODE_CODE=503 run_install install --file "$ROOT/fixture-v0.3.0.tar.gz" \
    --node-center "$CENTER" --node-token "$TOKEN")" \
    || fail "node install (503) must still succeed"
contains "$out" "中心未开启节点接入" "self-check 503 message"
out="$(STUB_NODE_CODE=401 run_install install --file "$ROOT/fixture-v0.3.0.tar.gz" \
    --node-center "$CENTER" --node-token wrong-token)" \
    || fail "node install (401) must still succeed"
contains "$out" "令牌不正确" "self-check 401 message"
out="$(run_install install --file "$ROOT/fixture-v0.3.0.tar.gz" \
    --node-center "$CENTER" --node-token "$TOKEN")" \
    || fail "node install (network failure) must still succeed"
contains "$out" "连不上" "self-check network-failure message"
contains "$out" "$CENTER" "self-check failure names the URL tried"

# --- 4. standalone install: hint line, no node keys -----------------
rm -rf "${ROOT:?}/etc" "${ROOT:?}/var" "${ROOT:?}/usr" "${ROOT:?}/opt"
out="$(run_install install --file "$ROOT/fixture-v0.2.0.tar.gz")" \
    || fail "standalone install exit"
env_lacks CENTER_URL "standalone install"
[ -f "$UNIT_DIR_TEST/dhtsearch-web.service" ] \
    || fail "standalone install: web unit missing"
contains "$out" "角色：独立" "standalone install role line"
contains "$out" "设置 → 节点接入" "standalone install center hint"

# --- 5. converting a standalone install into a node -----------------
out="$(STUB_NODE_CODE=200 run_install install --file "$ROOT/fixture-v0.2.0.tar.gz" \
    --node-center "$CENTER" --node-token "$TOKEN" --node-name hk-2)" \
    || fail "conversion install exit"
env_has CENTER_URL "$CENTER" "conversion"
env_has NODE_NAME "hk-2" "conversion"
contains "$out" "systemctl restart dhtsearch.service" "conversion restarts the API"
not_contains "$out" "systemctl restart dhtsearch.service dhtsearch-web.service" \
    "conversion does not restart the web service"

# --- 6. menu de-conversion removes the keys, web comes back ---------
# 解除节点模式 lives only in the menu, so drive it through a pty
# (same technique as test-menu.sh): 10 爬虫节点 → 2 解除 → y 确认.
if command -v script >/dev/null 2>&1; then
    out="$(printf '%b' '10\n2\ny\n\n0\n0\n' | DHTSEARCH_ROOT="$ROOT" \
        DHTSEARCH_SKIP_SYSTEMD=1 PATH="$STUB_BIN:$PATH" \
        script -qec "bash $INSTALL" /dev/null | tr -d '\r')" \
        || fail "menu de-conversion exit"
    env_lacks CENTER_URL "menu de-conversion"
    env_lacks NODE_TOKEN "menu de-conversion"
    env_lacks NODE_NAME "menu de-conversion"
    contains "$out" "已解除节点模式，当前角色：独立" "menu de-conversion result"
    [ -f "$UNIT_DIR_TEST/dhtsearch-web.service" ] \
        || fail "menu de-conversion: web unit not restored"
    contains "$out" "systemctl restart dhtsearch.service dhtsearch-web.service" \
        "menu de-conversion restarts both services again"
else
    echo "SKIP: 'script' (util-linux) is required for the menu de-conversion case"
fi

echo "node install tests: all passed"
