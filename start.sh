#!/usr/bin/env bash
# start.sh — run the whole stack locally with one command: Go API + Next.js
# frontend, wired together, with one Ctrl-C stopping both.
#
#   ./start.sh          full mode: the DHT crawler runs and the index grows
#   ./start.sh --demo   no crawling, a handful of demo rows inserted instead —
#                       the way to see a working site immediately, and the only
#                       way that works without outbound UDP
#   ./start.sh --prod   production build of the frontend instead of the dev
#                       server (slower to start, representative of production)
#
# Ports and paths come from the environment: API_PORT (8080), WEB_PORT (3000),
# DB_PATH (./data/dhtsearch.db). Everything else is read from .env — see
# env.example.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
API_PORT="${API_PORT:-8080}"
WEB_PORT="${WEB_PORT:-3000}"
DB_PATH="${DB_PATH:-$ROOT_DIR/data/dhtsearch.db}"
BIN_DIR="$ROOT_DIR/.run"
API_BIN="$BIN_DIR/dhtsearch-server"

MODE=dev
DEMO=0
for arg in "$@"; do
    case "$arg" in
        --demo) DEMO=1 ;;
        --prod) MODE=prod ;;
        -h|--help) sed -n '2,14p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "unknown option: $arg (try --help)" >&2; exit 2 ;;
    esac
done

say()  { printf '\033[32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m==>\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m==>\033[0m %s\n' "$*" >&2; exit 1; }

# --- prerequisites -----------------------------------------------------------

for cmd in go node npm curl; do
    command -v "$cmd" >/dev/null 2>&1 || die "$cmd is required but not installed"
done

# A port already in use is the most common reason the stack half-starts, and
# the resulting errors are indirect. Say so plainly up front instead.
port_busy() {
    if command -v lsof >/dev/null 2>&1; then
        lsof -iTCP:"$1" -sTCP:LISTEN -t >/dev/null 2>&1
    else
        # No lsof (common in slim containers): ask the port directly.
        curl -s --max-time 1 "http://127.0.0.1:$1/" >/dev/null 2>&1
    fi
}
for p in "$API_PORT:API_PORT" "$WEB_PORT:WEB_PORT"; do
    port="${p%%:*}"; name="${p##*:}"
    port_busy "$port" && die "port $port is already in use (set $name=... to change it)"
done

# --- configuration -----------------------------------------------------------

# The server resolves ENV_FILE relative to its working directory, and this
# script runs it from server/. Without pinning the path, a .env sitting at the
# repo root — where env.example and the README put it — is silently ignored,
# and every secret in it (OPENAI_API_KEY, ADMIN_PASSWORD) quietly does nothing.
ENV_FILE="$ROOT_DIR/.env"
if [ ! -f "$ENV_FILE" ]; then
    cp "$ROOT_DIR/env.example" "$ENV_FILE"
    say "created .env from env.example (gitignored — put your keys there)"
fi

mkdir -p "$(dirname "$DB_PATH")" "$BIN_DIR"

# --- build -------------------------------------------------------------------

say "building the API server"
(cd "$ROOT_DIR/server" && go build -o "$API_BIN" ./cmd/server)

if [ ! -d "$ROOT_DIR/web/node_modules" ]; then
    say "installing frontend dependencies (first run only)"
    (cd "$ROOT_DIR/web" && npm ci)
fi
if [ "$MODE" = prod ]; then
    say "building the frontend"
    (cd "$ROOT_DIR/web" && NEXT_PUBLIC_API_BASE="http://127.0.0.1:$API_PORT" npm run build)
fi

# --- process management ------------------------------------------------------

API_PID=""
WEB_PID=""

# descendants lists every process below pid, deepest first, so a tree can be
# signalled from the leaves up.
descendants() {
    local child
    for child in $(pgrep -P "$1" 2>/dev/null); do
        descendants "$child"
        printf '%s\n' "$child"
    done
}

# stop_tree kills a process and its entire subtree.
#
# One level is not enough: the frontend runs as subshell -> npm -> sh -> node
# -> next-server, and npm does not pass a TERM down. Signalling only the direct
# children leaves next-server alive and still holding the port — after which
# the next run of this script dies with "Another next dev server is already
# running". Process groups would be tidier, but setsid does not exist on macOS,
# and this repo is developed there.
stop_tree() {
    local pid="$1"
    [ -n "$pid" ] || return 0
    local pids
    pids="$(descendants "$pid") $pid"
    # shellcheck disable=SC2086
    kill -TERM $pids 2>/dev/null || true
    local i
    for i in $(seq 1 20); do
        # shellcheck disable=SC2086
        ps -p $pids >/dev/null 2>&1 || return 0
        sleep 0.25
    done
    # shellcheck disable=SC2086
    kill -KILL $pids 2>/dev/null || true
}

cleanup() {
    trap - EXIT INT TERM
    echo
    say "stopping"
    stop_tree "$WEB_PID"
    stop_tree "$API_PID"
    wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# --- run ---------------------------------------------------------------------

api_env=(
    "ENV_FILE=$ENV_FILE"
    "HTTP_ADDR=127.0.0.1:$API_PORT"
    "DB_PATH=$DB_PATH"
    # Plain HTTP locally, so a Secure cookie would never be sent back and the
    # admin console could not hold a session.
    "ADMIN_SECURE=false"
)
api_args=()
if [ "$DEMO" = 1 ]; then
    # No crawler means nothing ever reaches the metadata fetcher, so building
    # its torrent client is pure cost — and pure risk: it opens listening
    # sockets and fails outright in containers without IPv6, which is exactly
    # where someone reaches for demo mode.
    api_env+=("CRAWL_ENABLED=false" "FETCH_METADATA=false")
    api_args+=(--seed-demo)
    say "demo mode: no crawling, demo rows inserted"
fi

say "starting the API on http://127.0.0.1:$API_PORT"
env "${api_env[@]}" "$API_BIN" "${api_args[@]}" &
API_PID=$!

# wait_ready polls a URL until it answers, giving up if the process behind it
# dies or the deadline passes. Polling rather than sleeping matters at both
# ends: a first start on an existing database builds the full-text index before
# the server listens, and the Next dev server compiles for a few seconds — a
# fixed sleep would be either wrong or needlessly slow.
wait_ready() {
    local what="$1" url="$2" pid="$3" limit="$4"
    local deadline=$((SECONDS + limit))
    until curl -sf -o /dev/null --max-time 2 "$url" 2>/dev/null; do
        kill -0 "$pid" 2>/dev/null || die "$what exited during startup (scroll up for its error)"
        [ "$SECONDS" -lt "$deadline" ] || die "$what did not become ready within ${limit}s"
        sleep 0.3
    done
}

say "waiting for the API to be ready"
wait_ready "the API" "http://127.0.0.1:$API_PORT/api/healthz" "$API_PID" 180
say "API is ready"

say "starting the frontend on http://127.0.0.1:$WEB_PORT"
if [ "$MODE" = prod ]; then
    (cd "$ROOT_DIR/web" && NEXT_PUBLIC_API_BASE="http://127.0.0.1:$API_PORT" \
        npm run start -- --port "$WEB_PORT" --hostname 127.0.0.1) &
else
    (cd "$ROOT_DIR/web" && NEXT_PUBLIC_API_BASE="http://127.0.0.1:$API_PORT" \
        npm run dev -- --port "$WEB_PORT" --hostname 127.0.0.1) &
fi
WEB_PID=$!

# Wait for the frontend too before advertising its URL. Next compiles on
# startup, so printing the link the instant the process forks sends the first
# click to a refused connection.
wait_ready "the frontend" "http://127.0.0.1:$WEB_PORT/" "$WEB_PID" 300

echo
say "站点     http://127.0.0.1:$WEB_PORT"
say "API      http://127.0.0.1:$API_PORT/api/stats"
if grep -qE '^ADMIN_PASSWORD=.+' "$ENV_FILE" 2>/dev/null; then
    say "控制台   http://127.0.0.1:$API_PORT/admin"
else
    warn "控制台   未启用（在 .env 里设 ADMIN_PASSWORD 后重启本脚本）"
fi
if [ "$DEMO" = 0 ]; then
    warn "爬虫需要 UDP 出站，且索引要数小时才见规模；只想立刻看到界面请用 ./start.sh --demo"
fi
echo
say "Ctrl-C 停止全部"

# Exit as soon as either side dies, so a crashed backend does not leave a
# frontend serving errors against nothing. Polled rather than `wait -n`, which
# needs bash 4.3 — macOS still ships 3.2, and this repo has launchd units, so
# someone is running it there.
while kill -0 "$API_PID" 2>/dev/null && kill -0 "$WEB_PID" 2>/dev/null; do
    sleep 1
done
warn "one of the two processes exited; shutting the other down"
