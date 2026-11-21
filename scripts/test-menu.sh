#!/usr/bin/env bash
# test-menu.sh — hook-mode smoke tests for the interactive management
# menu in install.sh: the grouped rendering, the kv status header,
# the not-installed guards, the choice → handler mapping, and the
# background latest-version hint. No root, nothing written outside a
# temp DHTSEARCH_ROOT (the hint case stubs curl on PATH, so it makes
# no network calls either):
#
#   ./scripts/test-menu.sh
#
# The menu only opens on a terminal — main() prints usage when stdin
# is not a TTY — so choices are fed through a pty allocated by
# `script` (util-linux). The check-update case has no network
# dependency: do_check_update prints the current version before it
# ever queries GitHub. Exits non-zero on the first failed assertion.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
INSTALL="scripts/install.sh"

fail() { echo "FAIL: $1" >&2; exit 1; }

command -v script >/dev/null 2>&1 || { echo "SKIP: 'script' (util-linux) is required for the menu pty tests"; exit 0; }

ROOT="$(mktemp -d)"
trap 'rm -rf "$ROOT"' EXIT
DATA_DIR_TEST="$ROOT/var/lib/dhtsearch"
UNIT_DIR_TEST="$ROOT/etc/systemd/system"

# run_menu <input> — feed <input> (with \n escapes) to the menu in
# hook mode and print everything it printed, CRs stripped.
run_menu() {
    printf '%b' "$1" | DHTSEARCH_ROOT="$ROOT" DHTSEARCH_SKIP_SYSTEMD=1 \
        script -qec "bash $INSTALL" /dev/null | tr -d '\r'
}

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

# --- 1. fresh root: grouped menu, kv header, 未安装 state ------------
out="$(run_menu '0\n')" || fail "menu exit on '0' (fresh root)"
contains "$out" "DHTSearch 管理菜单" "menu title"
contains "$out" "状态：未安装（选 16 安装）" "not-installed header"
contains "$out" "系统：" "system header line"
not_contains "$out" "API 服务" "no service lines when not installed"
not_contains "$out" "发现新版本" "no update hint when not installed"
for section in 服务 更新 配置 数据 系统; do
    contains "$out" "$section" "section header $section"
done
contains "$out" "  1) 状态" "item 1"
contains "$out" "  6) 检查更新" "item 6"
contains "$out" "  7) 升级到最新版" "item 7"
# Inline state: auto-update is determinable from files alone, so it
# is annotated even in hook mode; 开机自启 needs systemd, so in hook
# mode it carries no annotation rather than a guessed one.
contains "$out" "  8) 自动更新设置 [关]" "item 8 with auto-update state"
contains "$out" "  10) 爬虫节点" "item 10 crawler node"
contains "$out" "  13) 开机自启" "item 13"
not_contains "$out" "开机自启 [" "item 13 unannotated in hook mode"
contains "$out" "  16) 安装 / 重新安装" "item 16"
contains "$out" "  17) 卸载" "item 17"
contains "$out" "  0) 退出" "item 0"
contains "$out" "请选择 [0-17]：" "ranged prompt"

# --- 2. not-installed guards: notice instead of the handler ---------
out="$(run_menu '1\n\n0\n')" || fail "menu exit after guarded 状态"
contains "$out" "尚未安装，请先选" "guard notice for 状态"
not_contains "$out" "Version: unknown" "do_status not invoked when not installed"
out="$(run_menu '5\n\n0\n')" || fail "menu exit after guarded 停止服务"
contains "$out" "尚未安装，请先选" "guard notice for 停止服务"
not_contains "$out" "确认停止" "menu_stop not invoked when not installed"

# --- 3. unknown choice re-prompts, EOF exits -------------------------
out="$(run_menu '99\n0\n')" || fail "menu exit after unknown choice"
contains "$out" "未知选择：99" "unknown choice message"
out="$(run_menu '')" || fail "menu exit on EOF"
contains "$out" "DHTSearch 管理菜单" "menu renders before EOF"

# --- 4. installed root: header shows version + auto-update ----------
mkdir -p "$DATA_DIR_TEST"
printf 'v0.1.11\n' > "$DATA_DIR_TEST/VERSION"
printf 'ENABLED=1\n' > "$DATA_DIR_TEST/autoupdate"
out="$(run_menu '0\n')" || fail "menu exit on '0' (installed root)"
contains "$out" "版本：v0.1.11" "installed version header"
contains "$out" "自动更新：开（每天 03:37）" "auto-update header"
contains "$out" "  8) 自动更新设置 [开]" "item 8 annotated on"
# Hook mode has no systemd and this root has no unit files: the
# service lines are omitted, never guessed from a real systemctl
# call against the host.
not_contains "$out" "API 服务" "service lines omitted in hook mode"
not_contains "$out" "运行中" "no guessed service state in hook mode"

# --- 5. remapped choice 6 reaches check-update -----------------------
# The extra lines decline the upgrade prompt (if one appears) and
# absorb the pause, then exit; the assertion text is printed before
# any network use, so this holds online and off.
out="$(run_menu '6\nn\n\n\n0\n')" || fail "menu exit after choice 6"
contains "$out" "当前版本：v0.1.11" "choice 6 runs check-update"

# --- 6. unit files seeded: header reports them as installed ---------
mkdir -p "$UNIT_DIR_TEST"
: > "$UNIT_DIR_TEST/dhtsearch.service"
: > "$UNIT_DIR_TEST/dhtsearch-web.service"
out="$(run_menu '0\n')" || fail "menu exit on '0' (unit files seeded)"
contains "$out" "API 服务：已安装（状态未知）" "API line from unit file"
contains "$out" "Web 服务：已安装（状态未知）" "Web line from unit file"

# --- 7. background latest-version lookup paints the hint ------------
# No DHTSEARCH_SKIP_SYSTEMD here — that hook suppresses the lookup —
# but curl is stubbed on PATH, so the "latest release" is a fixture
# and nothing touches the network. The repeated unknown choices
# force re-renders; the hint appears once the background job lands.
STUB_BIN="$ROOT/stub-bin"
mkdir -p "$STUB_BIN"
cat > "$STUB_BIN/curl" <<'EOF'
#!/bin/sh
# test stub: answer every request with a fixed latest-release body
printf '{"tag_name": "v9.9.9"}\n'
EOF
chmod +x "$STUB_BIN/curl"
out="$(printf '%b' '99\n99\n99\n0\n' | PATH="$STUB_BIN:$PATH" DHTSEARCH_ROOT="$ROOT" \
    script -qec "bash $INSTALL" /dev/null | tr -d '\r')" || fail "menu exit (hint case)"
contains "$out" "发现新版本 v9.9.9，选 7 升级到最新版" "update hint line"

# --- 8. 爬虫节点 submenu: role display follows the config file ------
# Standalone first: the submenu says 独立 and offers the three
# actions. (The guard requires an install; the VERSION file above
# already marks this root installed.)
out="$(run_menu '10\n0\n0\n')" || fail "menu exit after node submenu (standalone)"
contains "$out" "爬虫节点接入" "node submenu title"
contains "$out" "当前角色：独立" "node submenu standalone role"
contains "$out" "配置 / 修改节点接入" "node submenu configure action"
contains "$out" "连通性测试" "node submenu check action"
# Now seed the worker keys: the submenu and the header must both
# show the node role, with the token as set-only.
ENV_TEST="$ROOT/etc/dhtsearch/dhtsearch.env"
mkdir -p "$(dirname "$ENV_TEST")"
printf 'CENTER_URL=https://center.example:10800\nNODE_TOKEN=<redacted>\nNODE_NAME=hk-1\n' > "$ENV_TEST"
out="$(run_menu '10\n0\n0\n')" || fail "menu exit after node submenu (worker)"
contains "$out" "角色：爬虫节点（中心 https://center.example:10800，节点名 hk-1）" "header node role"
contains "$out" "当前角色：爬虫节点（中心 https://center.example:10800，节点名 hk-1）" "node submenu worker role"
contains "$out" "令牌：    已设置" "node submenu token state"
not_contains "$out" "secret-token-1234" "token value never printed in the menu"
rm -f "$ENV_TEST"

echo "menu tests: all passed"
