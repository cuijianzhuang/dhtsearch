#!/usr/bin/env bash
# test-node-wireguard.sh — hook-mode tests for the WireGuard node
# onboarding in install.sh: center wg-setup (idempotent, conf
# shape), wg-bundle IP allocation (.2, .3, ...) and bundle
# contents, a node joining via `install --join-bundle` (tunnel
# conf written from the bundle, node env pointed through the
# tunnel, handshake + API self-check), the distinct
# tunnel-not-established report, wg-remove freeing an IP for
# reuse, wg-add's manual path, the tokenless-bundle prompt path,
# and upgrade leaving the tunnel untouched.
# wg / wg-quick / curl are stubbed on PATH (the stub wg derives
# pubkeys from private keys with md5, so tests can recompute the
# expected values); everything lives under temp DHTSEARCH_ROOTs.
#
#   ./scripts/test-node-wireguard.sh
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
    # env_has <root> <key> <value> <what>
    grep -qE "^$2=$3\$" "$1/etc/dhtsearch/dhtsearch.env" \
        || fail "$4: env file should carry $2=$3"
}

field() {
    # field <decoded-bundle> <key> — one bundle field's value.
    printf '%s\n' "$1" | sed -n "s/^$2=//p" | tail -1
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
ROOTC="$WORK/center"
ROOTN="$WORK/node"
ROOTN2="$WORK/node2"
ROOTC2="$WORK/center2"
ROOTN3="$WORK/node3"
mkdir -p "$ROOTC" "$ROOTN" "$ROOTN2" "$ROOTC2" "$ROOTN3"

# Fixture release packages for the node-side installs.
make_pkg() {
    local dir="$WORK/pkg-src-$1/dhtsearch-$1-linux-amd64"
    mkdir -p "$dir/web/.next"
    printf 'binary %s\n' "$1" > "$dir/dhtsearch-server"
    printf '// standalone\n' > "$dir/web/server.js"
    printf 'build-%s\n' "$1" > "$dir/web/.next/BUILD_ID"
    printf '{}\n' > "$dir/web/.next/required-server-files.json"
    cp "$INSTALL" "$dir/install.sh"
    tar -czf "$WORK/fixture-$1.tar.gz" -C "$WORK/pkg-src-$1" "dhtsearch-$1-linux-amd64"
}
make_pkg v0.2.0
make_pkg v0.3.0

STUB_BIN="$WORK/stub-bin"
mkdir -p "$STUB_BIN"
cat > "$STUB_BIN/wg" <<'EOF'
#!/bin/sh
# Test stub for wg. genkey emits a real-shaped random key; pubkey
# derives a deterministic 44-char stand-in from the private key
# (md5 hex + padding) so tests can recompute expectations; show
# pretends the interface is up (latest-handshakes reports a fresh
# handshake unless STUB_WG_NO_HANDSHAKE is set); everything else
# (syncconf, ...) succeeds silently.
case "$1" in
    genkey)
        head -c 32 /dev/urandom | base64 | tr -d '\n'
        echo
        ;;
    pubkey)
        priv="$(cat)"
        h="$(printf '%s' "$priv" | md5sum | cut -d' ' -f1)"
        printf '%sAAAAAAAAAAA=\n' "$h"
        ;;
    show)
        if [ "${3:-}" = "latest-handshakes" ]; then
            if [ -n "${STUB_WG_NO_HANDSHAKE:-}" ]; then
                printf 'FAKEPUB\t0\n'
            else
                printf 'FAKEPUB\t%s\n' "$(date +%s)"
            fi
        fi
        exit 0
        ;;
    *)
        exit 0
        ;;
esac
EOF
cat > "$STUB_BIN/wg-quick" <<'EOF'
#!/bin/sh
# Test stub for wg-quick: strip prints the conf the installer
# wrote (wg syncconf's input); up/down/restart succeed silently.
case "$1" in
    strip) cat "${DHTSEARCH_ROOT}/etc/wireguard/wg-dht.conf" ;;
    *) exit 0 ;;
esac
EOF
cat > "$STUB_BIN/curl" <<'EOF'
#!/bin/sh
# Test stub for curl, same contract as test-node-install.sh:
# /api/node/check answers STUB_NODE_CODE (unset = network
# failure); everything else succeeds quietly (no output, so the
# endpoint detector finds nothing).
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
chmod +x "$STUB_BIN/wg" "$STUB_BIN/wg-quick" "$STUB_BIN/curl"

# run_as <root> <args...> — install.sh in hook mode against <root>.
run_as() {
    local root="$1"
    shift
    PATH="$STUB_BIN:$PATH" DHTSEARCH_ROOT="$root" DHTSEARCH_SKIP_SYSTEMD=1 \
        bash "$INSTALL" "$@"
}

CTOKEN=test-token-123
ENDPOINT="center.example.com"
WG_C="$ROOTC/etc/wireguard"
WG_N="$ROOTN/etc/wireguard"

# --- 1. center wg-setup: conf shape + idempotency -------------------
out="$(run_as "$ROOTC" wg-setup --endpoint "$ENDPOINT")" \
    || fail "wg-setup exit"
contains "$out" "中心隧道已开启" "wg-setup report"
contains "$out" "提醒：云厂商安全组" "wg-setup UDP reminder"
[ -s "$WG_C/wg-dht.key" ] || fail "wg-setup: center key file missing"
[ "$(stat -c %a "$WG_C/wg-dht.key")" = "600" ] \
    || fail "wg-setup: center key file must be 600"
grep -qx 'Address = 10.9.0.1/24' "$WG_C/wg-dht.conf" \
    || fail "wg-setup: conf Address"
grep -qx 'ListenPort = 51820' "$WG_C/wg-dht.conf" \
    || fail "wg-setup: conf ListenPort"
center_priv="$(cat "$WG_C/wg-dht.key")"
grep -qx "PrivateKey = $center_priv" "$WG_C/wg-dht.conf" \
    || fail "wg-setup: conf carries the center private key"
grep -qx "ENDPOINT=$ENDPOINT" "$WG_C/wg-dht.state" \
    || fail "wg-setup: state carries the endpoint"
conf_sum1="$(md5sum "$WG_C/wg-dht.conf" | cut -d' ' -f1)"
out="$(run_as "$ROOTC" wg-setup)" || fail "wg-setup rerun exit"
[ "$(cat "$WG_C/wg-dht.key")" = "$center_priv" ] \
    || fail "wg-setup rerun: keypair must be kept (idempotent)"
[ "$(md5sum "$WG_C/wg-dht.conf" | cut -d' ' -f1)" = "$conf_sum1" ] \
    || fail "wg-setup rerun: conf must be unchanged (idempotent)"
contains "$out" "${ENDPOINT}:51820" "wg-setup rerun reuses the stored endpoint"
center_pub="$(printf '%s' "$center_priv" | "$STUB_BIN/wg" pubkey)"

# --- 2. wg-bundle: allocation .2/.3 + bundle contents ---------------
mkdir -p "$ROOTC/etc/dhtsearch"
printf 'NODE_TOKEN=%s\n' "$CTOKEN" > "$ROOTC/etc/dhtsearch/dhtsearch.env"
out="$(run_as "$ROOTC" wg-bundle --name hk-1)" || fail "wg-bundle hk-1 exit"
contains "$out" "凭据警告" "wg-bundle credential warning"
B1="$(printf '%s\n' "$out" | grep -E '^[A-Za-z0-9+/=]{60,}$' | tail -1)"
[ -n "$B1" ] || fail "wg-bundle hk-1: no bundle line printed"
D1="$(printf '%s' "$B1" | base64 -d)"
[ "$(field "$D1" name)" = "hk-1" ] || fail "bundle hk-1: name field"
[ "$(field "$D1" ip)" = "10.9.0.2" ] || fail "bundle hk-1: first IP is .2"
[ "$(field "$D1" endpoint)" = "${ENDPOINT}:51820" ] \
    || fail "bundle hk-1: endpoint field"
[ "$(field "$D1" api_port)" = "8080" ] || fail "bundle hk-1: api_port field"
[ "$(field "$D1" token)" = "$CTOKEN" ] \
    || fail "bundle hk-1: token taken from the center env file"
[ "$(field "$D1" center_pubkey)" = "$center_pub" ] \
    || fail "bundle hk-1: center_pubkey field"
priv1="$(field "$D1" privkey)"
[ -n "$priv1" ] || fail "bundle hk-1: privkey field"
not_contains "$out" "$priv1" "node private key never printed outside the bundle"
pub1="$(printf '%s' "$priv1" | "$STUB_BIN/wg" pubkey)"
grep -qx "PEER hk-1 10.9.0.2 $pub1" "$WG_C/wg-dht.state" \
    || fail "bundle hk-1: state peer line"
grep -qx "PublicKey = $pub1" "$WG_C/wg-dht.conf" \
    || fail "bundle hk-1: conf peer pubkey"
grep -qx 'AllowedIPs = 10.9.0.2/32' "$WG_C/wg-dht.conf" \
    || fail "bundle hk-1: conf peer AllowedIPs"
out="$(run_as "$ROOTC" wg-bundle --name hk-2)" || fail "wg-bundle hk-2 exit"
B2="$(printf '%s\n' "$out" | grep -E '^[A-Za-z0-9+/=]{60,}$' | tail -1)"
D2="$(printf '%s' "$B2" | base64 -d)"
[ "$(field "$D2" ip)" = "10.9.0.3" ] || fail "bundle hk-2: second IP is .3"
if run_as "$ROOTC" wg-bundle --name hk-1 >/dev/null 2>&1; then
    fail "wg-bundle for an existing node must refuse"
fi

# --- 3. node joins with the bundle ----------------------------------
out="$(STUB_NODE_CODE=200 run_as "$ROOTN" install \
    --file "$WORK/fixture-v0.2.0.tar.gz" --join-bundle "$B1")" \
    || fail "join install exit"
env_has "$ROOTN" CENTER_URL "http://10.9.0.1:8080" "join install"
env_has "$ROOTN" NODE_NAME "hk-1" "join install"
env_has "$ROOTN" NODE_TOKEN "$CTOKEN" "join install"
grep -qx "PrivateKey = $priv1" "$WG_N/wg-dht.conf" \
    || fail "join: node conf private key from the bundle"
grep -qx 'Address = 10.9.0.2/32' "$WG_N/wg-dht.conf" \
    || fail "join: node conf address"
grep -qx "PublicKey = $center_pub" "$WG_N/wg-dht.conf" \
    || fail "join: node conf center pubkey"
grep -qx "Endpoint = ${ENDPOINT}:51820" "$WG_N/wg-dht.conf" \
    || fail "join: node conf endpoint"
grep -qx 'AllowedIPs = 10.9.0.1/32' "$WG_N/wg-dht.conf" \
    || fail "join: node conf AllowedIPs"
grep -qx 'PersistentKeepalive = 25' "$WG_N/wg-dht.conf" \
    || fail "join: node conf keepalive"
contains "$out" "systemctl restart wg-quick@wg-dht" "join restarts the tunnel unit"
contains "$out" "WireGuard 隧道已建立" "join handshake verdict"
contains "$out" "连通性测试：通" "join API self-check through the tunnel"
out="$(run_as "$ROOTN" status)" || fail "node status exit"
contains "$out" "Tunnel:  wg-dht 运行中" "node status tunnel line"
contains "$out" "最近握手" "node status handshake age"

# --- 4. tunnel down: distinct report, install still succeeds --------
out="$(STUB_WG_NO_HANDSHAKE=1 WG_HANDSHAKE_TRIES=2 STUB_NODE_CODE=200 \
    run_as "$ROOTN2" install --file "$WORK/fixture-v0.2.0.tar.gz" \
    --join-bundle "$B2" 2>&1)" || fail "join install (no handshake) must still succeed"
contains "$out" "WireGuard 隧道未建立" "no-handshake distinct report"
contains "$out" "UDP 51820" "no-handshake report names the port to check"

# --- 5. wg-remove frees the IP; center status counts peers ----------
out="$(run_as "$ROOTC" wg-remove --name hk-1)" || fail "wg-remove exit"
contains "$out" "已移除节点 hk-1" "wg-remove report"
if grep -q '^PEER hk-1 ' "$WG_C/wg-dht.state"; then
    fail "wg-remove: state peer line must be gone"
fi
if grep -q "PublicKey = $pub1" "$WG_C/wg-dht.conf"; then
    fail "wg-remove: conf peer must be gone"
fi
out="$(run_as "$ROOTC" wg-bundle --name hk-3)" || fail "wg-bundle hk-3 exit"
B3="$(printf '%s\n' "$out" | grep -E '^[A-Za-z0-9+/=]{60,}$' | tail -1)"
D3="$(printf '%s' "$B3" | base64 -d)"
[ "$(field "$D3" ip)" = "10.9.0.2" ] \
    || fail "wg-remove: the freed .2 is the next allocation"
out="$(run_as "$ROOTC" status)" || fail "center status exit"
contains "$out" "已注册节点 2 个" "center status peer count"

# --- 6. wg-add: the manual path --------------------------------------
manual_priv="$("$STUB_BIN/wg" genkey)"
manual_pub="$(printf '%s' "$manual_priv" | "$STUB_BIN/wg" pubkey)"
out="$(run_as "$ROOTC" wg-add --name manual-1 --pubkey "$manual_pub")" \
    || fail "wg-add exit"
grep -qx "PEER manual-1 10.9.0.4 $manual_pub" "$WG_C/wg-dht.state" \
    || fail "wg-add: next free IP after .2/.3 is .4"
if run_as "$ROOTC" wg-add --name manual-1 --pubkey "$manual_pub" >/dev/null 2>&1; then
    fail "wg-add for an existing node must refuse"
fi
if run_as "$ROOTC" wg-add --name other --pubkey "$manual_pub" --ip 10.9.0.4 >/dev/null 2>&1; then
    fail "wg-add with a taken IP must refuse"
fi

# --- 7. tokenless bundle + the node-side prompt ----------------------
out="$(run_as "$ROOTC2" wg-setup --endpoint c2.example.com)" \
    || fail "wg-setup (center2) exit"
out="$(run_as "$ROOTC2" wg-bundle --name solo-1)" \
    || fail "wg-bundle (tokenless) exit"
contains "$out" "未带令牌" "tokenless bundle is called out"
B0="$(printf '%s\n' "$out" | grep -E '^[A-Za-z0-9+/=]{60,}$' | tail -1)"
D0="$(printf '%s' "$B0" | base64 -d)"
if ! printf '%s\n' "$D0" | grep -qx 'token='; then
    fail "tokenless bundle: token field must be present and empty"
fi
if command -v script >/dev/null 2>&1; then
    out="$(printf '%b' 'pty-token-123\n' | DHTSEARCH_ROOT="$ROOTN3" \
        DHTSEARCH_SKIP_SYSTEMD=1 PATH="$STUB_BIN:$PATH" STUB_NODE_CODE=200 \
        script -qec "bash $INSTALL install --file $WORK/fixture-v0.2.0.tar.gz --join-bundle $B0" /dev/null \
        | tr -d '\r')" || fail "join install (pty token prompt) exit"
    env_has "$ROOTN3" NODE_TOKEN "pty-token-123" "token prompt path"
    env_has "$ROOTN3" CENTER_URL "http://10.9.0.1:8080" "token prompt path"
else
    echo "SKIP: 'script' (util-linux) is required for the token prompt case"
fi

# --- 8. upgrade leaves the tunnel and the node role untouched --------
node_conf_sum="$(md5sum "$WG_N/wg-dht.conf" | cut -d' ' -f1)"
out="$(run_as "$ROOTN" upgrade --file "$WORK/fixture-v0.3.0.tar.gz")" \
    || fail "node upgrade exit"
[ "$(md5sum "$WG_N/wg-dht.conf" | cut -d' ' -f1)" = "$node_conf_sum" ] \
    || fail "upgrade must not touch the tunnel conf"
env_has "$ROOTN" CENTER_URL "http://10.9.0.1:8080" "node upgrade"
env_has "$ROOTN" NODE_TOKEN "$CTOKEN" "node upgrade"
grep -qx 'v0.3.0' "$ROOTN/var/lib/dhtsearch/VERSION" \
    || fail "node upgrade: VERSION not updated"

# --- 9. garbage bundle is refused before anything happens ------------
if run_as "$ROOTN2" install --file "$WORK/fixture-v0.2.0.tar.gz" \
    --join-bundle 'not-a-real-bundle!!!' >/dev/null 2>&1; then
    fail "a malformed join bundle must fail the install"
fi

# --- 10. menu paths: center tunnel submenu + paste-bundle re-join ---
if command -v script >/dev/null 2>&1; then
    run_menu() {
        # run_menu <root> <input>
        local root="$1" input="$2"
        printf '%b' "$input" | DHTSEARCH_ROOT="$root" \
            DHTSEARCH_SKIP_SYSTEMD=1 PATH="$STUB_BIN:$PATH" \
            STUB_NODE_CODE=200 \
            script -qec "bash $INSTALL" /dev/null | tr -d '\r'
    }
    # The center root needs a real install before the menu lets it
    # into 爬虫节点； the tunnel state in /etc/wireguard is untouched
    # by it (exactly the upgrade/install survival property).
    out="$(run_as "$ROOTC" install --file "$WORK/fixture-v0.2.0.tar.gz")" \
        || fail "center standalone install exit"
    [ -n "$(grep -E '^PEER ' "$WG_C/wg-dht.state")" ] \
        || fail "install must not touch the tunnel peer state"
    out="$(run_menu "$ROOTC" '10\n5\n0\n0\n0\n')" \
        || fail "center tunnel menu exit"
    contains "$out" "中心隧道（WireGuard" "center tunnel submenu title"
    contains "$out" "已注册节点" "center tunnel submenu peer list"
    contains "$out" "manual-1" "center tunnel submenu lists peers"
    # Re-join the node through the menu with hk-3's bundle: the
    # tunnel conf and the node keys are replaced wholesale.
    out="$(run_menu "$ROOTN" "10\n4\n$B3\ny\n\n0\n0\n")" \
        || fail "menu re-join exit"
    env_has "$ROOTN" NODE_NAME "hk-3" "menu re-join replaces the node name"
    grep -qx 'Address = 10.9.0.2/32' "$WG_N/wg-dht.conf" \
        || fail "menu re-join: node conf carries the new bundle's IP"
    contains "$out" "WireGuard 隧道已建立" "menu re-join handshake verdict"
else
    echo "SKIP: 'script' (util-linux) is required for the menu cases"
fi

echo "node wireguard tests: all passed"
