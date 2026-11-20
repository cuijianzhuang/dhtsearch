#!/usr/bin/env bash
# test-menu.sh — hook-mode smoke tests for the interactive management
# menu in install.sh: the grouped rendering, the status header line,
# and the choice → handler mapping. No root, no systemd, nothing
# written outside a temp DHTSEARCH_ROOT:
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

# --- 1. fresh root: grouped menu, 未安装 header, 0 exits -------------
out="$(run_menu '0\n')" || fail "menu exit on '0' (fresh root)"
contains "$out" "DHTSearch 管理菜单" "menu title"
contains "$out" "当前：未安装" "not-installed header"
for section in 服务 更新 配置 数据 系统; do
    contains "$out" "$section" "section header $section"
done
contains "$out" "  1) 状态" "item 1"
contains "$out" "  6) 检查更新" "item 6"
contains "$out" "  7) 升级到最新版" "item 7"
contains "$out" "  15) 安装 / 重新安装" "item 15"
contains "$out" "  16) 卸载" "item 16"
contains "$out" "  0) 退出" "item 0"

# --- 2. unknown choice re-prompts, EOF exits -------------------------
out="$(run_menu '99\n0\n')" || fail "menu exit after unknown choice"
contains "$out" "未知选择：99" "unknown choice message"
out="$(run_menu '')" || fail "menu exit on EOF"
contains "$out" "DHTSearch 管理菜单" "menu renders before EOF"

# --- 3. installed root: header shows version + auto-update -----------
mkdir -p "$DATA_DIR_TEST"
printf 'v0.1.11\n' > "$DATA_DIR_TEST/VERSION"
printf 'ENABLED=1\n' > "$DATA_DIR_TEST/autoupdate"
out="$(run_menu '0\n')" || fail "menu exit on '0' (installed root)"
contains "$out" "当前：v0.1.11 · 自动更新 开" "installed header"
# Hook mode has no systemd: the service piece must be omitted, never
# guessed from a real systemctl call.
not_contains "$out" "· 服务" "service piece omitted in hook mode"

# --- 4. remapped choice 6 reaches check-update -----------------------
# The extra lines decline the upgrade prompt (if one appears) and
# absorb the pause, then exit; the assertion text is printed before
# any network use, so this holds online and off.
out="$(run_menu '6\nn\n\n\n0\n')" || fail "menu exit after choice 6"
contains "$out" "当前版本：v0.1.11" "choice 6 runs check-update"

echo "menu tests: all passed"
