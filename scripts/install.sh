#!/usr/bin/env bash
# install.sh — one-click install / upgrade / uninstall for DHTSearch.
#
#   bash <(curl -fsSL https://raw.githubusercontent.com/cuijianzhuang/dhtsearch/main/scripts/install.sh) install
#
# Subcommands: install | upgrade | uninstall | status
# Run with no arguments in a terminal for an interactive menu.
# install also copies this script to /usr/local/bin/dhtsearch, so on an
# installed machine plain `dhtsearch` re-opens the menu (status / restart /
# logs / upgrade / change ports / switch database / uninstall) without
# re-downloading it.
#
# Database: SQLite by default. install can instead pick PostgreSQL —
# --db postgres-local installs and configures a local server, --db
# postgres-url takes an existing one — and the management menu's
# 数据库切换 moves between backends later, migrating the data with
# the server binary's `migrate` subcommand.
# That file is the management script; the service binary is
# dhtsearch-server and the two never overwrite each other.
#
# What gets installed (two systemd services, matching deploy.sh's topology):
#   dhtsearch.service       Go API + crawler   /usr/local/bin/dhtsearch-server
#   dhtsearch-web.service   Next.js standalone node /opt/dhtsearch/web/server.js
#
# Layout:
#   config   /etc/dhtsearch/dhtsearch.env   (preserved across install/upgrade)
#   data     /var/lib/dhtsearch/            (SQLite db, backups, VERSION)
#   logs     journalctl -u dhtsearch [-u dhtsearch-web]
#
# Packages come from GitHub Releases (built by .github/workflows/release.yml
# via scripts/package-release.sh) and are verified against the published
# sha256 before anything is installed. --from-source is a fallback for
# versions that have no release assets; it needs git, Go and Node on the
# target machine.
#
# Testing hooks (not needed on a real server):
#   DHTSEARCH_ROOT=/tmp/root   prefix every install path (unit *contents*
#                              still point at the real paths)
#   DHTSEARCH_SKIP_SYSTEMD=1   write unit files but never call systemctl
#   DHTSEARCH_DRY_RUN=1        same as --dry-run
set -euo pipefail

REPO="cuijianzhuang/dhtsearch"
SERVICE_USER="dhtsearch"
HEALTH_TIMEOUT=120

# Logical paths (also what the systemd units contain).
L_BIN="/usr/local/bin/dhtsearch-server"
L_MANAGER="/usr/local/bin/dhtsearch"
L_WEB_DIR="/opt/dhtsearch/web"
L_CONF_DIR="/etc/dhtsearch"
L_ENV_FILE="/etc/dhtsearch/dhtsearch.env"
L_DATA_DIR="/var/lib/dhtsearch"

# Filesystem paths (prefixed under DHTSEARCH_ROOT when testing).
PREFIX="${DHTSEARCH_ROOT:-}"
BIN_FILE="${PREFIX}${L_BIN}"
MANAGER_FILE="${PREFIX}${L_MANAGER}"
WEB_DIR="${PREFIX}${L_WEB_DIR}"
APP_DIR="${PREFIX}/opt/dhtsearch"
CONF_DIR="${PREFIX}${L_CONF_DIR}"
ENV_FILE="${PREFIX}${L_ENV_FILE}"
DATA_DIR="${PREFIX}${L_DATA_DIR}"
BACKUP_DIR="${DATA_DIR}/backups"
VERSION_FILE="${DATA_DIR}/VERSION"
UNIT_DIR="${PREFIX}/etc/systemd/system"
API_UNIT="dhtsearch.service"
WEB_UNIT="dhtsearch-web.service"

VERSION=""
API_PORT_OPT=""
WEB_PORT_OPT=""
DB_OPT=""
DATABASE_URL_OPT=""
# Resolved by resolve_database during install: the DATABASE_URL the
# config file should carry ("" = SQLite) and whether the choice was
# explicit (--db flag or the interactive prompt), as opposed to "keep
# whatever the existing config already has".
RESOLVED_DATABASE_URL=""
DB_CHOICE_EXPLICIT=0
LOCAL_PG_URL=""
LOCAL_PG_PASSWORD_NEW=0
CLEANUP_DIR=""
cleanup_tmp() { if [ -n "$CLEANUP_DIR" ]; then rm -rf "$CLEANUP_DIR"; fi; }
trap cleanup_tmp EXIT
FROM_SOURCE=0
LOCAL_FILE=""
PURGE=0
DRY_RUN="${DHTSEARCH_DRY_RUN:-0}"

log() { printf '==> %s\n' "$*"; }
warn() { printf 'WARN: %s\n' "$*" >&2; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
Usage: install.sh <command> [options]

Commands:
  install              download a release package and install (idempotent)
  upgrade              like install, but back up the old binary/web/env
                       first and roll back automatically if the health
                       check fails
  uninstall            stop services and remove binary/web/units;
                       config and data are kept unless --purge is given
  status               show service state, installed version, health
  help                 this text

Options (install / upgrade):
  --version vX.Y.Z     release tag to install (default: latest release)
  --from-source        build from source instead of downloading a release
                       (requires git, Go and Node.js on this machine)
  --file PATH          install from a local release tarball
  --api-port PORT      API listen port (default: $API_PORT, else the
                       port already in the config file, else 8080).
                       Written to HTTP_ADDR in the config file and handed
                       to the web service as API_BASE, so the frontend
                       follows the backend to the new port.
  --web-port PORT      web listen port (default: $WEB_PORT, else the
                       port already configured, else 3000)
  --db MODE            database backend for a fresh install (default:
                       sqlite; an existing config's backend is kept
                       unless --db is given explicitly):
                         sqlite          single file in the data dir
                         postgres-local  install PostgreSQL on this
                                         machine (apt: Debian/Ubuntu),
                                         create the dhtsearch role and
                                         database, generate a password
                         postgres-url    use an existing PostgreSQL;
                                         requires --database-url and is
                                         connection-tested before the
                                         config is written
                       Upgrade never changes the database; switch later
                       from the `dhtsearch` menu (数据库切换), which
                       also migrates the data.
  --database-url URL   postgres:// connection URL for --db postgres-url
  --dry-run            write config/unit files but do not touch systemd
                       or start services

Options (uninstall):
  --purge              also delete /etc/dhtsearch and /var/lib/dhtsearch

Environment:
  API_PORT / WEB_PORT    default ports when the matching --*-port flag
                         is not given (same priority rules as above)
  DHTSEARCH_ROOT         prefix all install paths (testing)
  DHTSEARCH_SKIP_SYSTEMD=1
                         write units without calling systemctl (testing)

On an installed machine, run `dhtsearch` with no arguments for the
interactive management menu (this script, copied to
/usr/local/bin/dhtsearch by install; the service binary is
dhtsearch-server).
EOF
}

# ---------------------------------------------------------------- checks

require_root() {
    if [ "$(id -u)" -ne 0 ]; then
        die "must run as root (try: sudo bash install.sh ...)"
    fi
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) die "unsupported architecture: $(uname -m) (need amd64 or arm64)" ;;
    esac
}

check_platform() {
    [ "$(uname -s)" = "Linux" ] || die "Linux only"
    if [ -z "$PREFIX" ] && ! command -v systemctl >/dev/null 2>&1; then
        die "systemctl not found; this installer manages systemd services"
    fi
    if ! command -v node >/dev/null 2>&1; then
        die "Node.js runtime not found: the web frontend is a Next.js standalone server and needs node (>= 18) on PATH. Install Node.js, then re-run."
    fi
}

have_systemd() {
    [ "${DHTSEARCH_SKIP_SYSTEMD:-0}" != "1" ] && [ "$DRY_RUN" != "1" ] \
        && command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]
}

# ---------------------------------------------------------------- config

valid_port() {
    case "$1" in
        ''|*[!0-9]*) return 1 ;;
    esac
    [ "$1" -ge 1 ] && [ "$1" -le 65535 ]
}

# env_value KEY — value of the last KEY= line in the config file ("" when
# the file or the key is missing).
env_value() {
    local line=""
    if [ -f "$ENV_FILE" ]; then
        line="$(grep -E "^$1=" "$ENV_FILE" | tail -1 || true)"
    fi
    echo "${line#"$1"=}"
}

api_port() {
    local addr
    addr="$(env_value HTTP_ADDR)"
    [ -n "$addr" ] || addr="127.0.0.1:8080"
    echo "${addr##*:}"
}

web_port() {
    local p
    p="$(env_value WEB_PORT)"
    if [ -z "$p" ] && [ -f "${UNIT_DIR}/${WEB_UNIT}" ]; then
        p="$(sed -n 's/^Environment=PORT=//p' "${UNIT_DIR}/${WEB_UNIT}" | tail -1)"
    fi
    echo "${p:-3000}"
}

# Port resolution priority: --*-port flag > API_PORT / WEB_PORT
# environment > value already in the config file > default 8080 / 3000.
resolve_api_port() {
    if [ -n "$API_PORT_OPT" ]; then echo "$API_PORT_OPT"; return; fi
    if [ -n "${API_PORT:-}" ]; then echo "$API_PORT"; return; fi
    api_port
}

resolve_web_port() {
    if [ -n "$WEB_PORT_OPT" ]; then echo "$WEB_PORT_OPT"; return; fi
    if [ -n "${WEB_PORT:-}" ]; then echo "$WEB_PORT"; return; fi
    web_port
}

validate_ports() {
    local ap wp
    ap="$(resolve_api_port)"
    wp="$(resolve_web_port)"
    valid_port "$ap" || die "invalid API port: '$ap' (must be a number 1-65535; see --api-port)"
    valid_port "$wp" || die "invalid web port: '$wp' (must be a number 1-65535; see --web-port)"
    [ "$ap" != "$wp" ] || die "API port and web port must differ (both resolved to $ap)"
}

# set_env_value KEY VALUE — replace the KEY= line in the config file, or
# append it when missing.
set_env_value() {
    if grep -qE "^$1=" "$ENV_FILE"; then
        sed -i -E "s|^$1=.*|$1=$2|" "$ENV_FILE"
    else
        printf '%s=%s\n' "$1" "$2" >> "$ENV_FILE"
    fi
}

# remove_env_value KEY — delete the KEY= line from the config file.
remove_env_value() {
    [ -f "$ENV_FILE" ] || return 0
    sed -i -E "/^$1=/d" "$ENV_FILE"
}

# ---------------------------------------------------------------- database

# db_backend — "postgres" when the config carries a DATABASE_URL, else
# "sqlite".
db_backend() {
    if [ -n "$(env_value DATABASE_URL)" ]; then echo "postgres"; else echo "sqlite"; fi
}

# sqlite_db_path — the SQLite file the config selects (DB_PATH, with
# the standard data-dir default).
sqlite_db_path() {
    local p
    p="$(env_value DB_PATH)"
    echo "${p:-${L_DATA_DIR}/dhtsearch.db}"
}

# mask_pg_url URL — the URL with its password replaced by ***, safe to
# print in summaries and logs.
mask_pg_url() {
    echo "$1" | sed -E 's#(postgres(ql)?://[^:/@]+:)[^@]*@#\1***@#'
}

# db_display — one-line description of the configured database.
db_display() {
    if [ "$(db_backend)" = "postgres" ]; then
        echo "PostgreSQL ($(mask_pg_url "$(env_value DATABASE_URL)"))"
    else
        echo "SQLite ($(sqlite_db_path))"
    fi
}

valid_pg_url() {
    case "$1" in
        postgres://*|postgresql://*) return 0 ;;
        *) return 1 ;;
    esac
}

LOCAL_PG_USER="dhtsearch"
LOCAL_PG_DB="dhtsearch"

# pg_su CMD... — run a command as the postgres OS user (peer auth).
pg_su() {
    su postgres -c "$*"
}

# pg_local_works — true when a local PostgreSQL answers over the
# superuser's peer connection.
pg_local_works() {
    command -v psql >/dev/null 2>&1 || return 1
    pg_su "psql -tAc 'SELECT 1'" >/dev/null 2>&1
}

# install_postgres_apt — install the distro PostgreSQL packages. Only
# the apt path (Debian/Ubuntu) is automated; anything else gets a clear
# stop with manual instructions instead of a half-configured system.
install_postgres_apt() {
    if ! command -v apt-get >/dev/null 2>&1; then
        die "PostgreSQL is not installed and this system has no apt-get, so it cannot be installed automatically.
Install PostgreSQL manually (server + a database and login role named dhtsearch), then either:
  - re-run install with --db postgres-url --database-url postgres://dhtsearch:...@127.0.0.1:5432/dhtsearch, or
  - install with the default SQLite backend and switch later from the dhtsearch menu."
    fi
    log "Installing PostgreSQL via apt (this can take a few minutes)"
    apt-get update -qq || warn "apt-get update failed; trying the install anyway"
    DEBIAN_FRONTEND=noninteractive apt-get install -y postgresql \
        || die "apt-get install postgresql failed; install PostgreSQL manually and re-run (see the --db postgres-url option)"
}

# start_local_postgres — make sure the local server is up and ready.
start_local_postgres() {
    if command -v pg_isready >/dev/null 2>&1 && pg_isready -q >/dev/null 2>&1; then
        return 0
    fi
    systemctl start postgresql >/dev/null 2>&1 || service postgresql start >/dev/null 2>&1 || true
    local _
    for _ in $(seq 1 30); do
        if pg_isready -q >/dev/null 2>&1; then return 0; fi
        sleep 1
    done
    die "PostgreSQL is installed but did not come up (pg_isready still failing); check: systemctl status postgresql"
}

# ensure_local_postgres — reuse a working local PostgreSQL or install
# one, create the dhtsearch role + database, and set LOCAL_PG_URL.
# When the existing config already carries a local dhtsearch URL, its
# password is kept (and re-applied to the role) so nothing that already
# works is invalidated; otherwise a fresh password is generated and
# LOCAL_PG_PASSWORD_NEW is set so the caller can show it exactly once.
ensure_local_postgres() {
    if ! pg_local_works; then
        install_postgres_apt
    fi
    start_local_postgres
    pg_local_works || die "PostgreSQL did not answer after install/start; check: systemctl status postgresql"

    local port pw existing
    port="$(pg_su "psql -tAc 'SHOW port'" | tr -d '[:space:]')"
    [ -n "$port" ] || port="5432"

    existing="$(env_value DATABASE_URL)"
    pw=""
    if [ -n "$existing" ]; then
        pw="$(echo "$existing" | sed -nE "s#^postgres(ql)?://${LOCAL_PG_USER}:([^@]*)@.*#\2#p")"
    fi
    if [ -n "$pw" ]; then
        LOCAL_PG_PASSWORD_NEW=0
    else
        if command -v openssl >/dev/null 2>&1; then
            pw="$(openssl rand -hex 24)"
        else
            pw="$(od -An -tx1 -N24 /dev/urandom | tr -d ' \n')"
        fi
        LOCAL_PG_PASSWORD_NEW=1
    fi

    # The password is hex-only by construction, so it is safe inside
    # the single-quoted SQL literal below.
    if [ "$(pg_su "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='${LOCAL_PG_USER}'\"" | tr -d '[:space:]')" = "1" ]; then
        pg_su "psql -c \"ALTER ROLE ${LOCAL_PG_USER} WITH LOGIN PASSWORD '${pw}'\"" >/dev/null \
            || die "failed to set the password of the PostgreSQL role ${LOCAL_PG_USER}"
    else
        pg_su "psql -c \"CREATE ROLE ${LOCAL_PG_USER} LOGIN PASSWORD '${pw}'\"" >/dev/null \
            || die "failed to create the PostgreSQL role ${LOCAL_PG_USER}"
    fi
    if [ "$(pg_su "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='${LOCAL_PG_DB}'\"" | tr -d '[:space:]')" != "1" ]; then
        pg_su "createdb -O ${LOCAL_PG_USER} ${LOCAL_PG_DB}" \
            || die "failed to create the PostgreSQL database ${LOCAL_PG_DB}"
    fi
    LOCAL_PG_URL="postgres://${LOCAL_PG_USER}:${pw}@127.0.0.1:${port}/${LOCAL_PG_DB}?sslmode=disable"
}

# resolve_database — settle which backend a fresh install uses and set
# RESOLVED_DATABASE_URL / DB_CHOICE_EXPLICIT. An explicit --db (or the
# interactive prompt, which sets DB_OPT the same way) wins; without one
# an existing config's backend is kept untouched.
resolve_database() {
    LOCAL_PG_URL=""
    LOCAL_PG_PASSWORD_NEW=0
    if [ -n "$DB_OPT" ]; then
        DB_CHOICE_EXPLICIT=1
        case "$DB_OPT" in
            sqlite) RESOLVED_DATABASE_URL="" ;;
            postgres-url)
                [ -n "$DATABASE_URL_OPT" ] || die "--db postgres-url needs --database-url"
                valid_pg_url "$DATABASE_URL_OPT" || die "invalid --database-url: must start with postgres:// or postgresql://"
                RESOLVED_DATABASE_URL="$DATABASE_URL_OPT"
                ;;
            postgres-local)
                ensure_local_postgres
                RESOLVED_DATABASE_URL="$LOCAL_PG_URL"
                ;;
        esac
    else
        DB_CHOICE_EXPLICIT=0
        RESOLVED_DATABASE_URL="$(env_value DATABASE_URL)"
    fi
}

# probe_pg_url URL — prove the URL is connectable (server reachable,
# credentials accepted) before any config is written, by running the
# release binary's migrate subcommand from the URL into a throwaway
# SQLite file: it opens the source (which also creates the schema) and
# copies zero rows. Needs PKG_TARBALL from obtain_package.
probe_pg_url() {
    local url="$1" tmp bin
    tmp="$(mktemp -d)"
    if ! tar -xzf "$PKG_TARBALL" -C "$tmp" --wildcards --no-anchored 'dhtsearch-server' 2>/dev/null; then
        rm -rf "$tmp"
        die "could not extract dhtsearch-server from the package to test the database connection"
    fi
    bin="$(find "$tmp" -name dhtsearch-server -type f | head -1)"
    chmod +x "$bin"
    # Bounded: a binary that does not understand the migrate
    # subcommand (or a half-dead server) must fail the probe, never
    # hang the install.
    if timeout 60 "$bin" migrate --from "$url" --to "$tmp/probe.db" >/dev/null 2>&1; then
        rm -rf "$tmp"
        return 0
    fi
    rm -rf "$tmp"
    return 1
}

# apply_ports — write the resolved ports back into an existing config
# file. Only the port part of HTTP_ADDR changes; a custom host part is
# preserved. Without explicit port options this rewrites the values
# already in the file, so upgrade / repeat install never silently fall
# back to the default ports.
apply_ports() {
    local ap wp addr host
    ap="$(resolve_api_port)"
    wp="$(resolve_web_port)"
    addr="$(env_value HTTP_ADDR)"
    host="${addr%:*}"
    set_env_value HTTP_ADDR "${host:-127.0.0.1}:${ap}"
    set_env_value WEB_PORT "$wp"
}

# apply_db_choice — write an explicit install-time database choice
# into an existing config file (fresh files get it via the template in
# write_env_file). SQLite removes the DATABASE_URL line; the DB_PATH
# file itself is left alone and stays the switch-back target.
apply_db_choice() {
    if [ -n "$RESOLVED_DATABASE_URL" ]; then
        set_env_value DATABASE_URL "$RESOLVED_DATABASE_URL"
    else
        remove_env_value DATABASE_URL
    fi
}

write_env_file() {
    if [ -f "$ENV_FILE" ]; then
        log "Config exists, keeping it: $ENV_FILE"
        apply_ports
        if [ "$DB_CHOICE_EXPLICIT" = "1" ]; then
            apply_db_choice
        fi
        return
    fi
    local ap wp db_url_line
    ap="$(resolve_api_port)"
    wp="$(resolve_web_port)"
    if [ -n "$RESOLVED_DATABASE_URL" ]; then
        db_url_line="DATABASE_URL=${RESOLVED_DATABASE_URL}"
    else
        db_url_line="#DATABASE_URL=postgres://user:password@127.0.0.1:5432/dhtsearch?sslmode=disable"
    fi
    log "Writing default config: $ENV_FILE"
    cat > "$ENV_FILE" <<EOF
# DHTSearch configuration — full reference: env.example in the repo/release.
# Values here are read by systemd (EnvironmentFile) and by the server itself.
# This file can carry a live database password, so it is kept mode 600
# (root-only); systemd reads it as root for the services.

# API + crawler. Loopback by default; put a reverse proxy in front for
# public access. The web service fetches this address server-side.
HTTP_ADDR=127.0.0.1:${ap}

# Web frontend port, remembered here so install.sh and the dhtsearch
# menu keep it across upgrades. The API server ignores this value; the
# web systemd unit's PORT and API_BASE are generated from these ports.
WEB_PORT=${wp}

# Database: SQLite by default — a single file in the data dir.
DB_PATH=${L_DATA_DIR}/dhtsearch.db

# PostgreSQL instead of SQLite: when DATABASE_URL is set it wins and
# DB_PATH is ignored (the file stays as the switch-back target).
# Chosen at install time via --db, or later from the dhtsearch menu
# (数据库切换), which also migrates the data. Tables/indexes are
# created on startup.
${db_url_line}

# Fixed DHT UDP port so a router port-forward can target it. Inbound UDP
# is what passive discovery lives on; 0 would pick a new port each start.
DHT_PORT=46881

# Throughput knobs (measured defaults; see README before changing).
DHT_SAMPLERS=8
META_WORKERS=128
META_TIMEOUT=45s

# LLM moderation (optional): without a key the pass stays off and only
# the static keyword filter runs.
#OPENAI_API_KEY=
#OPENAI_BASE_URL=https://openrouter.ai/api/v1
#OPENAI_MODEL=nvidia/nemotron-3-super-120b-a12b:free
EOF
    chmod 600 "$ENV_FILE"
}

write_units() {
    local node_bin ap wp
    node_bin="$(command -v node)"
    ap="$(resolve_api_port)"
    wp="$(resolve_web_port)"
    log "Writing systemd units to $UNIT_DIR"
    cat > "${UNIT_DIR}/${API_UNIT}" <<EOF
[Unit]
Description=DHTSearch API and DHT crawler
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
WorkingDirectory=${L_DATA_DIR}
EnvironmentFile=${L_ENV_FILE}
Environment=ENV_FILE=${L_ENV_FILE}
ExecStart=${L_BIN}
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=${L_DATA_DIR}

[Install]
WantedBy=multi-user.target
EOF
    cat > "${UNIT_DIR}/${WEB_UNIT}" <<EOF
[Unit]
Description=DHTSearch web frontend (Next.js standalone)
After=network-online.target ${API_UNIT}
Wants=network-online.target
Requires=${API_UNIT}

[Service]
Type=simple
User=${SERVICE_USER}
WorkingDirectory=${L_WEB_DIR}
Environment=NODE_ENV=production
Environment=PORT=${wp}
Environment=HOSTNAME=127.0.0.1
Environment=API_BASE=http://127.0.0.1:${ap}
ExecStart=${node_bin} ${L_WEB_DIR}/server.js
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=${L_WEB_DIR}

[Install]
WantedBy=multi-user.target
EOF
}

# ------------------------------------------------------------ port check

# port_listeners PORT — ss output lines for TCP listeners on PORT
# (empty when the port is free or ss is unavailable).
port_listeners() {
    command -v ss >/dev/null 2>&1 || return 0
    ss -tlnp 2>/dev/null | grep -E "[:.]$1[[:space:]]" || true
}

# listener_is_ours PORT UNIT — true when the listener on PORT is
# UNIT itself. Repeat install / upgrade while our own services already
# hold the target ports is normal, not a conflict. Two ways to tell:
# pid attribution from ss (not always available — a restricted /proc
# hides other services' pids), or, when the unit is active, probing the
# port and checking that what answers is actually our service.
listener_is_ours() {
    local port="$1" unit="$2" mainpid pids pid
    have_systemd || return 1
    if systemctl is-active --quiet "$unit" 2>/dev/null; then
        case "$unit" in
            "$API_UNIT")
                curl -fsS "http://127.0.0.1:${port}/api/healthz" >/dev/null 2>&1 && return 0
                ;;
            "$WEB_UNIT")
                curl -fsS "http://127.0.0.1:${port}/" 2>/dev/null | grep -qi "DHTSearch" && return 0
                ;;
        esac
    fi
    mainpid="$(systemctl show -p MainPID --value "$unit" 2>/dev/null || echo 0)"
    { [ -n "$mainpid" ] && [ "$mainpid" != "0" ]; } || return 1
    pids="$(port_listeners "$port" | grep -oE 'pid=[0-9]+' | cut -d= -f2 | sort -u)"
    [ -n "$pids" ] || return 1
    for pid in $pids; do
        [ "$pid" = "$mainpid" ] || return 1
    done
    return 0
}

# check_port_free PORT UNIT LABEL — die, naming the occupying process,
# when PORT is held by anything other than UNIT itself.
check_port_free() {
    local port="$1" unit="$2" label="$3" lines
    lines="$(port_listeners "$port")"
    [ -n "$lines" ] || return 0
    listener_is_ours "$port" "$unit" && return 0
    die "${label} port ${port} is already in use by another process:
${lines}
Free the port first, or pick a different one (--api-port / --web-port,
or the port prompts in the interactive menu)."
}

# check_ports_free — fail fast, before anything is downloaded or
# restarted, instead of surfacing later as a 120s health-check timeout
# with the services crash-looping on a busy port.
check_ports_free() {
    have_systemd || return 0
    check_port_free "$(resolve_api_port)" "$API_UNIT" "API"
    check_port_free "$(resolve_web_port)" "$WEB_UNIT" "Web"
}

# ---------------------------------------------------------------- systemd

svc() {
    if have_systemd; then
        systemctl "$@"
    else
        log "(systemd skipped) systemctl $*"
    fi
}

restart_services() {
    svc daemon-reload
    svc enable --now "$API_UNIT" "$WEB_UNIT" >/dev/null 2>&1 || true
    svc restart "$API_UNIT" "$WEB_UNIT"
}

wait_health() {
    local port webport deadline
    port="$(resolve_api_port)"
    webport="$(resolve_web_port)"
    deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
    log "Waiting for http://127.0.0.1:${port}/api/healthz (up to ${HEALTH_TIMEOUT}s)"
    if [ "$DRY_RUN" = "1" ] || [ "${DHTSEARCH_SKIP_SYSTEMD:-0}" = "1" ]; then
        log "(skipped in dry-run / no-systemd mode)"
        return 0
    fi
    while [ "$(date +%s)" -lt "$deadline" ]; do
        if curl -fsS "http://127.0.0.1:${port}/api/healthz" >/dev/null 2>&1; then
            log "Healthy: /api/healthz OK on port ${port}"
            break
        fi
        sleep 2
    done
    if [ "$(date +%s)" -ge "$deadline" ]; then
        warn "Health check failed after ${HEALTH_TIMEOUT}s. Last log lines:"
        if have_systemd; then
            journalctl -u "$API_UNIT" -n 50 --no-pager >&2 || true
        fi
        return 1
    fi
    # The API being up is not enough: the web unit can crash-loop on its
    # own (bad WorkingDirectory perms, missing node) while healthz passes.
    while [ "$(date +%s)" -lt "$deadline" ]; do
        if curl -fsS "http://127.0.0.1:${webport}/" >/dev/null 2>&1; then
            log "Healthy: web OK on port ${webport}"
            return 0
        fi
        sleep 2
    done
    warn "Web check failed after ${HEALTH_TIMEOUT}s. Last log lines:"
    if have_systemd; then
        journalctl -u "$WEB_UNIT" -n 50 --no-pager >&2 || true
    fi
    return 1
}

# ---------------------------------------------------------------- package

normalize_version() {
    local v="$1"
    case "$v" in v*) echo "$v" ;; *) echo "v$v" ;; esac
}

resolve_latest_version() {
    local tag
    tag="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
    [ -n "$tag" ] || die "could not resolve latest release from GitHub API; use --version to pin one"
    echo "$tag"
}

# download_package <version> <arch> <dest-dir>; sets PKG_TARBALL.
download_package() {
    local version="$1" arch="$2" dest="$3"
    local name="dhtsearch-${version}-linux-${arch}"
    local base="https://github.com/${REPO}/releases/download/${version}"
    log "Downloading ${name}.tar.gz from GitHub Release ${version}"
    curl -fsSL -o "${dest}/${name}.tar.gz" "${base}/${name}.tar.gz" \
        || die "download failed. If no release has been published yet, build one with a v* tag (see .github/workflows/release.yml) or use --from-source."
    curl -fsSL -o "${dest}/${name}.tar.gz.sha256" "${base}/${name}.tar.gz.sha256" \
        || die "sha256 file missing for ${version}; refusing to install unverified package"
    (cd "$dest" && sha256sum -c "${name}.tar.gz.sha256") \
        || die "sha256 verification failed for ${name}.tar.gz"
    PKG_TARBALL="${dest}/${name}.tar.gz"
}

# build_package_from_source <version-or-empty> <arch> <dest-dir>; sets PKG_TARBALL and RESOLVED_VERSION.
build_package_from_source() {
    local version="$1" arch="$2" dest="$3"
    for tool in git go npm node; do
        command -v "$tool" >/dev/null 2>&1 || die "--from-source needs $tool on this machine"
    done
    local src="${dest}/src"
    log "Cloning ${REPO} (source build fallback)"
    if [ -n "$version" ]; then
        git clone --depth 1 --branch "$version" "https://github.com/${REPO}.git" "$src" \
            || die "git clone --branch $version failed"
    else
        git clone --depth 1 "https://github.com/${REPO}.git" "$src"
        version="dev-$(git -C "$src" rev-parse --short HEAD)"
    fi
    [ -f "$src/scripts/package-release.sh" ] \
        || die "this source tree has no scripts/package-release.sh; cannot build a package"
    (cd "$src" && ./scripts/package-release.sh --version "$version" --arch "$arch" --out "$dest")
    RESOLVED_VERSION="$version"
    PKG_TARBALL="${dest}/dhtsearch-${version}-linux-${arch}.tar.gz"
}

# install_package_files <tarball>
install_package_files() {
    local tarball="$1"
    local extract_dir
    extract_dir="$(mktemp -d)"
    tar -xzf "$tarball" -C "$extract_dir"
    local pkg_dir
    pkg_dir="$(find "$extract_dir" -mindepth 1 -maxdepth 1 -type d | head -1)"
    if [ -z "$pkg_dir" ] || [ ! -f "$pkg_dir/dhtsearch-server" ]; then
        die "package layout unexpected: no dhtsearch-server in $tarball"
    fi
    if [ ! -f "$pkg_dir/web/server.js" ]; then
        die "package layout unexpected: no web/server.js in $tarball"
    fi
    # The Next.js standalone server cannot start without its .next build
    # output; a package missing it (as v0.1.0/v0.1.1 were) installs fine
    # but the web service then crash-loops. Refuse it up front instead.
    if [ ! -f "$pkg_dir/web/.next/BUILD_ID" ] || [ ! -f "$pkg_dir/web/.next/required-server-files.json" ]; then
        die "package is corrupt: web/.next build output (BUILD_ID / required-server-files.json) missing in $tarball; the web frontend cannot start from this package — re-download it, or install a fixed release"
    fi

    install -m 0755 "$pkg_dir/dhtsearch-server" "$BIN_FILE"
    rm -rf "$WEB_DIR"
    mkdir -p "$APP_DIR"
    cp -a "$pkg_dir/web" "$WEB_DIR"
    rm -rf "$extract_dir"
    log "Installed binary -> $BIN_FILE, web -> $WEB_DIR"
}

prepare_dirs_and_user() {
    mkdir -p "$CONF_DIR" "$DATA_DIR" "$BACKUP_DIR" "$APP_DIR" "$UNIT_DIR" "$(dirname "$BIN_FILE")"
    if [ -z "$PREFIX" ] && ! id "$SERVICE_USER" >/dev/null 2>&1; then
        log "Creating system user $SERVICE_USER"
        useradd --system --home-dir "$L_DATA_DIR" --shell /usr/sbin/nologin "$SERVICE_USER"
    fi
    if [ -z "$PREFIX" ]; then
        chown -R "${SERVICE_USER}:${SERVICE_USER}" "$DATA_DIR" "$APP_DIR" || true
    fi
}

# Copied package files inherit whatever modes the tarball/extraction
# produced (and the caller's umask); the services run as SERVICE_USER, so
# normalize ownership and traversal perms or the web unit dies with
# status=200/CHDIR on WorkingDirectory.
fix_permissions() {
    chmod 755 "$APP_DIR" "$WEB_DIR" || true
    if [ -n "$PREFIX" ]; then return 0; fi
    if id "$SERVICE_USER" >/dev/null 2>&1; then
        chown -R "${SERVICE_USER}:${SERVICE_USER}" "$DATA_DIR" "$APP_DIR" || true
        if [ -f "$ENV_FILE" ]; then
            # The env file can carry a live database password (the
            # postgres-local flow generates one into DATABASE_URL), so
            # it is root-only 600. That does not starve the services:
            # systemd reads EnvironmentFile as root and passes the
            # values in the process environment, which is also what
            # the server checks first.
            chown root:root "$ENV_FILE" || true
            chmod 600 "$ENV_FILE" || true
        fi
    fi
}

record_version() {
    printf '%s\n' "$1" > "$VERSION_FILE"
}

installed_version() {
    if [ -f "$VERSION_FILE" ]; then cat "$VERSION_FILE"; else echo "unknown"; fi
}

# install_manager — copy this script to /usr/local/bin/dhtsearch so an
# installed machine gets a stable management entry point: plain
# `dhtsearch` opens the interactive menu. When the script was piped in
# (bash <(curl ...)), $0 is not a regular file, so fetch the published
# copy instead. This is the management script only; the service binary
# dhtsearch-server is a different file and is never touched here.
install_manager() {
    if [ -f "$0" ]; then
        if install -m 0755 "$0" "$MANAGER_FILE"; then
            log "Management entry installed: run 'dhtsearch' for the menu"
        else
            warn "could not install the management entry to $MANAGER_FILE"
        fi
    elif curl -fsSL "https://raw.githubusercontent.com/${REPO}/main/scripts/install.sh" -o "${MANAGER_FILE}.tmp" 2>/dev/null; then
        chmod 755 "${MANAGER_FILE}.tmp"
        mv "${MANAGER_FILE}.tmp" "$MANAGER_FILE"
        log "Management entry installed: run 'dhtsearch' for the menu"
    else
        rm -f "${MANAGER_FILE}.tmp"
        warn "could not install the management entry ($MANAGER_FILE); download install.sh and re-run install to get it"
    fi
}

# obtain_package <arch> <workdir>; sets PKG_TARBALL and RESOLVED_VERSION.
PKG_TARBALL=""
RESOLVED_VERSION=""
obtain_package() {
    local arch="$1" workdir="$2"
    if [ -n "$LOCAL_FILE" ]; then
        [ -f "$LOCAL_FILE" ] || die "--file not found: $LOCAL_FILE"
        RESOLVED_VERSION="${VERSION:-local}"
        cp "$LOCAL_FILE" "${workdir}/package.tar.gz"
        PKG_TARBALL="${workdir}/package.tar.gz"
    elif [ "$FROM_SOURCE" -eq 1 ]; then
        local v=""
        [ -n "$VERSION" ] && v="$(normalize_version "$VERSION")"
        build_package_from_source "$v" "$arch" "$workdir"
    else
        local v
        if [ -n "$VERSION" ]; then v="$(normalize_version "$VERSION")"; else v="$(resolve_latest_version)"; fi
        RESOLVED_VERSION="$v"
        download_package "$v" "$arch" "$workdir"
    fi
}

# ---------------------------------------------------------------- commands

do_install() {
    require_root
    check_platform
    validate_ports
    check_ports_free
    local arch workdir tarball
    arch="$(detect_arch)"
    workdir="$(mktemp -d)"
    CLEANUP_DIR="$workdir"

    prepare_dirs_and_user
    obtain_package "$arch" "$workdir"
    tarball="$PKG_TARBALL"
    # A bare --database-url (no --db) means postgres-url.
    if [ -n "$DATABASE_URL_OPT" ] && [ -z "$DB_OPT" ]; then
        DB_OPT="postgres-url"
    fi
    # Database choice comes before any file is written: postgres-local
    # may install and configure a whole PostgreSQL here, and an
    # unreachable postgres-url aborts the install before the config
    # (or the services) are touched.
    resolve_database
    if [ -n "$RESOLVED_DATABASE_URL" ] && [ "$DB_CHOICE_EXPLICIT" = "1" ]; then
        probe_pg_url "$RESOLVED_DATABASE_URL" \
            || die "cannot connect to PostgreSQL at $(mask_pg_url "$RESOLVED_DATABASE_URL") (server unreachable or credentials rejected); config not written, nothing installed"
        log "PostgreSQL connection verified: $(mask_pg_url "$RESOLVED_DATABASE_URL")"
    fi
    install_package_files "$tarball"
    write_env_file
    write_units
    fix_permissions
    restart_services
    if ! wait_health; then
        die "install finished but the service is not healthy; see logs: journalctl -u $API_UNIT -n 100"
    fi
    record_version "$RESOLVED_VERSION"
    install_manager
    log "Installed DHTSearch $RESOLVED_VERSION (arch $arch)"
    log "API: http://127.0.0.1:$(resolve_api_port)/api/healthz   Web: http://127.0.0.1:$(resolve_web_port)/"
    log "Database: $(db_display)"
    if [ "$LOCAL_PG_PASSWORD_NEW" = "1" ]; then
        log "Local PostgreSQL configured. Connection URL, shown only this once — save it:"
        log "  $RESOLVED_DATABASE_URL"
        log "  (it is also stored in $ENV_FILE, readable by root only)"
    fi
    log "Config: $ENV_FILE   Data: $DATA_DIR   Logs: journalctl -u $API_UNIT -f"
    log "Manage: run 'dhtsearch' for the interactive menu"
}

do_upgrade() {
    require_root
    [ -f "$BIN_FILE" ] || die "not installed yet; run install first"
    if [ -n "$DB_OPT" ]; then
        warn "--db is ignored by upgrade: the database backend is never changed here. Use the dhtsearch menu (数据库切换) to switch, which also migrates the data."
        DB_OPT=""
    fi
    # Never let a database resolution from an earlier action in the
    # same (menu) process leak into upgrade's config rewrite.
    DB_CHOICE_EXPLICIT=0
    RESOLVED_DATABASE_URL=""
    check_platform
    validate_ports
    check_ports_free
    local arch workdir tarball ts old_version
    arch="$(detect_arch)"
    workdir="$(mktemp -d)"
    CLEANUP_DIR="$workdir"
    ts="$(date +%Y%m%d%H%M%S)"
    old_version="$(installed_version)"

    obtain_package "$arch" "$workdir"
    tarball="$PKG_TARBALL"

    prepare_dirs_and_user
    log "Backing up current install ($old_version) to $BACKUP_DIR ($ts)"
    cp -a "$BIN_FILE" "${BACKUP_DIR}/dhtsearch-server.${ts}.bak"
    [ -f "$ENV_FILE" ] && cp -a "$ENV_FILE" "${BACKUP_DIR}/dhtsearch.env.${ts}.bak"
    [ -d "$WEB_DIR" ] && cp -a "$WEB_DIR" "${BACKUP_DIR}/web.${ts}.bak"

    install_package_files "$tarball"
    write_env_file
    write_units
    fix_permissions
    restart_services
    if wait_health; then
        record_version "$RESOLVED_VERSION"
        install_manager
        log "Upgraded: $old_version -> $RESOLVED_VERSION"
        return 0
    fi

    warn "New version not healthy; rolling back to $old_version"
    install -m 0755 "${BACKUP_DIR}/dhtsearch-server.${ts}.bak" "$BIN_FILE"
    if [ -d "${BACKUP_DIR}/web.${ts}.bak" ]; then
        rm -rf "$WEB_DIR"
        cp -a "${BACKUP_DIR}/web.${ts}.bak" "$WEB_DIR"
    fi
    restart_services
    if wait_health; then
        die "upgrade failed and was rolled back to $old_version; backup kept in $BACKUP_DIR"
    fi
    die "upgrade failed AND rollback is not healthy; backup in $BACKUP_DIR, logs: journalctl -u $API_UNIT -n 100"
}

do_uninstall() {
    require_root
    if have_systemd; then
        systemctl disable --now "$API_UNIT" "$WEB_UNIT" >/dev/null 2>&1 || true
    fi
    rm -f "${UNIT_DIR}/${API_UNIT}" "${UNIT_DIR}/${WEB_UNIT}"
    svc daemon-reload
    rm -f "$BIN_FILE"
    rm -f "$MANAGER_FILE"
    rm -rf "$APP_DIR"
    log "Removed services, binary, web files and the 'dhtsearch' management entry."
    if [ "$PURGE" -eq 1 ]; then
        rm -rf "$CONF_DIR" "$DATA_DIR"
        if [ -z "$PREFIX" ] && id "$SERVICE_USER" >/dev/null 2>&1; then
            userdel "$SERVICE_USER" || true
        fi
        log "--purge: config and data deleted."
    else
        log "Kept config ($ENV_FILE) and data ($DATA_DIR). Re-run with --purge to delete them too."
    fi
}

do_status() {
    echo "Version: $(installed_version)"
    echo "Binary:  $BIN_FILE"
    echo "Config:  $ENV_FILE"
    echo "Data:    $DATA_DIR"
    echo "Database: $(db_display)"
    echo "Ports:   API $(resolve_api_port)   Web $(resolve_web_port)"
    if have_systemd; then
        systemctl is-active "$API_UNIT" "$WEB_UNIT" || true
        systemctl status "$API_UNIT" "$WEB_UNIT" --no-pager -n 5 || true
    else
        echo "systemd: skipped (dry-run / no-systemd mode)"
    fi
    local port webport
    port="$(resolve_api_port)"
    webport="$(resolve_web_port)"
    if curl -fsS "http://127.0.0.1:${port}/api/healthz" >/dev/null 2>&1; then
        echo "Health:  API OK (http://127.0.0.1:${port}/api/healthz)"
    else
        echo "Health:  API not responding on port ${port}"
    fi
    if curl -fsS "http://127.0.0.1:${webport}/" >/dev/null 2>&1; then
        echo "Health:  Web OK (http://127.0.0.1:${webport}/)"
    else
        echo "Health:  Web not responding on port ${webport}"
    fi
}

# ------------------------------------------------------- interactive menu
# Everything below only runs when the script is started with no
# arguments on a terminal (directly, or via the installed `dhtsearch`
# entry point). The subcommand paths never prompt.

confirm() {
    local ans
    printf '%s [y/N] ' "$1"
    read -r ans || return 1
    case "$ans" in
        y|Y|yes|YES) return 0 ;;
        *) echo "已取消。"; return 1 ;;
    esac
}

# prompt_port LABEL DEFAULT — ask until a valid port is entered (empty
# input takes DEFAULT, invalid input re-asks). The chosen port goes to
# stdout; all prompting goes to stderr so the value can be captured.
prompt_port() {
    local label="$1" def="$2" v
    while true; do
        printf '%s（默认 %s）: ' "$label" "$def" >&2
        if ! read -r v; then return 1; fi
        v="${v:-$def}"
        if valid_port "$v"; then
            echo "$v"
            return 0
        fi
        echo "端口无效：必须是 1-65535 的数字，请重新输入。" >&2
    done
}

menu_pause() {
    printf '按回车返回菜单...'
    read -r _ || true
}

# prompt_pg_url — ask until a postgres:// URL is entered. The URL goes
# to stdout; prompting goes to stderr so the value can be captured.
prompt_pg_url() {
    local v
    while true; do
        printf 'PostgreSQL 连接地址（postgres://user:pass@host:5432/db）: ' >&2
        if ! read -r v; then return 1; fi
        if valid_pg_url "$v"; then
            echo "$v"
            return 0
        fi
        echo "地址无效：必须以 postgres:// 或 postgresql:// 开头。" >&2
    done
}

# menu_prompt_db — the install-time database question. Sets DB_OPT /
# DATABASE_URL_OPT exactly like the --db / --database-url flags.
menu_prompt_db() {
    local c
    echo "数据库选择："
    echo "  1) SQLite（默认，零配置，单文件）"
    echo "  2) PostgreSQL（本机自动安装并配置）"
    echo "  3) PostgreSQL（已有，填连接地址）"
    printf '选择（默认 1）: '
    read -r c || c="1"
    case "$c" in
        2) DB_OPT="postgres-local" ;;
        3)
            DB_OPT="postgres-url"
            DATABASE_URL_OPT="$(prompt_pg_url)" || return 1
            ;;
        *) DB_OPT="sqlite" ;;
    esac
    return 0
}

# db_choice_summary — how the chosen database reads in the install
# summary, before the config file exists to describe it.
db_choice_summary() {
    case "$DB_OPT" in
        postgres-local) echo "PostgreSQL（本机自动安装）" ;;
        postgres-url) echo "PostgreSQL ($(mask_pg_url "$DATABASE_URL_OPT"))" ;;
        *) echo "SQLite" ;;
    esac
}

menu_install() {
    local ap wp
    # Reset any database choice from an earlier action in this menu
    # session; it is re-asked below for a fresh config, and an existing
    # config must not have a stale choice applied to it.
    DB_OPT=""
    DATABASE_URL_OPT=""
    ap="$(prompt_port "API 端口" "$(resolve_api_port)")" || return 0
    wp="$(prompt_port "Web 端口" "$(resolve_web_port)")" || return 0
    if [ "$ap" = "$wp" ]; then
        echo "API 端口和 Web 端口不能相同。"
        menu_pause
        return 0
    fi
    # The database question only makes sense for a fresh config; with
    # an existing one the backend is kept (switching lives in its own
    # menu entry, with data migration).
    local db_summary
    if [ -f "$ENV_FILE" ]; then
        db_summary="$(db_display)（保持不变）"
    else
        menu_prompt_db || return 0
        db_summary="$(db_choice_summary)"
    fi
    echo
    echo "安装汇总："
    echo "  版本：    ${VERSION:-最新 Release}"
    echo "  API 端口：$ap"
    echo "  Web 端口：$wp"
    echo "  数据库：  $db_summary"
    echo "  配置文件：$ENV_FILE"
    echo "  数据目录：$DATA_DIR"
    if ! confirm "确认开始安装？"; then menu_pause; return 0; fi
    API_PORT_OPT="$ap"
    WEB_PORT_OPT="$wp"
    do_install
    menu_pause
}

menu_upgrade() {
    echo "当前版本：$(installed_version)"
    if ! confirm "确认升级？（先备份当前版本，健康检查失败自动回滚）"; then menu_pause; return 0; fi
    do_upgrade
    menu_pause
}

menu_restart() {
    if ! confirm "确认重启 API 和 Web 两个服务？"; then menu_pause; return 0; fi
    validate_ports
    check_ports_free
    restart_services
    wait_health || true
    menu_pause
}

menu_logs() {
    local c
    echo "查看日志： 1) API   2) Web   3) 两个都看   0) 返回"
    printf '选择： '
    read -r c || return 0
    case "$c" in
        1) journalctl -u "$API_UNIT" -n 100 --no-pager || true ;;
        2) journalctl -u "$WEB_UNIT" -n 100 --no-pager || true ;;
        3) journalctl -u "$API_UNIT" -u "$WEB_UNIT" -n 100 --no-pager || true ;;
        *) return 0 ;;
    esac
    menu_pause
}

menu_change_port() {
    require_root
    if [ ! -f "$ENV_FILE" ]; then
        echo "尚未安装（找不到 $ENV_FILE），请先选「安装」。"
        menu_pause
        return 0
    fi
    local ap wp nap nwp
    ap="$(resolve_api_port)"
    wp="$(resolve_web_port)"
    echo "当前端口：API $ap   Web $wp"
    nap="$(prompt_port "新 API 端口" "$ap")" || return 0
    nwp="$(prompt_port "新 Web 端口" "$wp")" || return 0
    if [ "$nap" = "$nwp" ]; then
        echo "API 端口和 Web 端口不能相同。"
        menu_pause
        return 0
    fi
    if [ "$nap" = "$ap" ] && [ "$nwp" = "$wp" ]; then
        echo "端口未变化。"
        menu_pause
        return 0
    fi
    API_PORT_OPT="$nap"
    WEB_PORT_OPT="$nwp"
    validate_ports
    echo "将改为：API $nap   Web $nwp（保存后重启两个服务）"
    if ! confirm "确认修改端口？"; then menu_pause; return 0; fi
    check_ports_free
    apply_ports
    write_units
    fix_permissions
    restart_services
    if wait_health; then
        log "端口已修改：API $nap   Web $nwp"
    else
        warn "改端口后健康检查未通过，请看日志：journalctl -u $API_UNIT -n 100"
    fi
    menu_pause
}

# switch_database TARGET [URL] — move the live install to another
# backend, migrating the data. The order is strict: stop the services,
# migrate with the server binary and let it verify row counts, only
# then rewrite DATABASE_URL, restart, and health-check. If migration
# or verification fails the config is never touched and the services
# come back up on the old backend; if the new backend fails its health
# check the config change is rolled back the same way (the source data
# is still intact — migration copies, it never deletes).
switch_database() {
    local target="$1" url="${2:-}"
    local src dst old_url
    old_url="$(env_value DATABASE_URL)"
    if [ "$(db_backend)" = "postgres" ]; then
        src="$old_url"
    else
        src="$(sqlite_db_path)"
    fi
    case "$target" in
        postgres-local)
            ensure_local_postgres
            dst="$LOCAL_PG_URL"
            ;;
        postgres-url) dst="$url" ;;
        sqlite) dst="$(sqlite_db_path)" ;;
        *) warn "未知切换目标：$target"; return 1 ;;
    esac

    log "停止服务（切换期间短暂不可用）"
    svc stop "$WEB_UNIT" "$API_UNIT"
    log "迁移数据：$(db_display) -> ${target}"
    if ! "$BIN_FILE" migrate --from "$src" --to "$dst"; then
        warn "数据迁移失败或行数核对未通过，配置未做任何修改。按原数据库恢复服务。"
        restart_services
        wait_health || true
        return 1
    fi
    if [ "$target" = "sqlite" ]; then
        remove_env_value DATABASE_URL
    else
        set_env_value DATABASE_URL "$dst"
    fi
    fix_permissions
    restart_services
    if wait_health; then
        log "数据库已切换：$(db_display)"
        if [ "$LOCAL_PG_PASSWORD_NEW" = "1" ]; then
            log "本机 PostgreSQL 连接地址（含密码，只显示这一次，请保存）："
            log "  $dst"
        fi
        return 0
    fi
    warn "切换后健康检查未通过，回滚到原数据库配置。"
    if [ -n "$old_url" ]; then
        set_env_value DATABASE_URL "$old_url"
    else
        remove_env_value DATABASE_URL
    fi
    fix_permissions
    restart_services
    wait_health || true
    return 1
}

menu_switch_db() {
    require_root
    if [ ! -f "$ENV_FILE" ]; then
        echo "尚未安装（找不到 $ENV_FILE），请先选「安装」。"
        menu_pause
        return 0
    fi
    if [ ! -f "$BIN_FILE" ]; then
        echo "服务二进制不存在（$BIN_FILE），请先安装或升级。"
        menu_pause
        return 0
    fi
    echo "当前数据库：$(db_display)"
    local target="" url="" c
    if [ "$(db_backend)" = "sqlite" ]; then
        echo "切换到："
        echo "  1) PostgreSQL（本机自动安装并配置）"
        echo "  2) PostgreSQL（已有，填连接地址）"
        echo "  0) 取消"
        printf '选择： '
        read -r c || return 0
        case "$c" in
            1) target="postgres-local" ;;
            2)
                target="postgres-url"
                url="$(prompt_pg_url)" || return 0
                ;;
            *) echo "已取消。"; menu_pause; return 0 ;;
        esac
    else
        echo "切换到："
        echo "  1) SQLite（数据迁回数据库文件）"
        echo "  0) 取消"
        printf '选择： '
        read -r c || return 0
        case "$c" in
            1) target="sqlite" ;;
            *) echo "已取消。"; menu_pause; return 0 ;;
        esac
    fi
    echo "切换流程：停服务 → 迁移数据并核对行数 → 改配置 → 重启并健康检查。"
    echo "期间服务会短暂停机；迁移失败会按原配置自动恢复，数据不丢。"
    if [ "$target" = "sqlite" ] && [ -f "$(sqlite_db_path)" ]; then
        echo "注意：目标 SQLite 文件已存在，迁移时同 info_hash 的旧行不会被覆盖。"
    fi
    if ! confirm "确认切换数据库？"; then menu_pause; return 0; fi
    if switch_database "$target" "$url"; then
        echo "切换完成。"
    else
        echo "切换未完成，已按原配置恢复（详情看上面的输出）。"
    fi
    menu_pause
}

menu_uninstall() {
    local c
    echo "卸载方式： 1) 保留配置和数据   2) 全部删除（--purge，连数据库一起删）   0) 取消"
    printf '选择： '
    read -r c || return 0
    case "$c" in
        1) PURGE=0 ;;
        2) PURGE=1 ;;
        *) echo "已取消。"; menu_pause; return 0 ;;
    esac
    if [ "$PURGE" -eq 1 ]; then
        if ! confirm "确认卸载并删除全部配置和数据？此操作不可恢复"; then menu_pause; return 0; fi
    else
        if ! confirm "确认卸载？（配置和数据会保留）"; then menu_pause; return 0; fi
    fi
    do_uninstall
    echo "卸载完成。"
    exit 0
}

menu() {
    local choice
    while true; do
        echo
        echo "DHTSearch 管理菜单"
        echo "  1) 安装"
        echo "  2) 升级"
        echo "  3) 状态"
        echo "  4) 重启服务"
        echo "  5) 查看日志"
        echo "  6) 修改端口"
        echo "  7) 数据库切换"
        echo "  8) 卸载"
        echo "  0) 退出"
        printf '选择： '
        if ! read -r choice; then exit 0; fi
        case "$choice" in
            1) menu_install ;;
            2) menu_upgrade ;;
            3) do_status; menu_pause ;;
            4) menu_restart ;;
            5) menu_logs ;;
            6) menu_change_port ;;
            7) menu_switch_db ;;
            8) menu_uninstall ;;
            0) exit 0 ;;
            *) echo "未知选择：$choice" ;;
        esac
    done
}

# ---------------------------------------------------------------- main

parse_opts() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --version) VERSION="$2"; shift 2 ;;
            --from-source) FROM_SOURCE=1; shift ;;
            --file) LOCAL_FILE="$2"; shift 2 ;;
            --api-port)
                [ $# -ge 2 ] || die "--api-port needs a value"
                API_PORT_OPT="$2"
                valid_port "$API_PORT_OPT" || die "invalid --api-port: '$API_PORT_OPT' (must be a number 1-65535)"
                shift 2 ;;
            --web-port)
                [ $# -ge 2 ] || die "--web-port needs a value"
                WEB_PORT_OPT="$2"
                valid_port "$WEB_PORT_OPT" || die "invalid --web-port: '$WEB_PORT_OPT' (must be a number 1-65535)"
                shift 2 ;;
            --db)
                [ $# -ge 2 ] || die "--db needs a value (sqlite | postgres-local | postgres-url)"
                DB_OPT="$2"
                case "$DB_OPT" in
                    sqlite|postgres-local|postgres-url) ;;
                    *) die "invalid --db: '$DB_OPT' (want sqlite | postgres-local | postgres-url)" ;;
                esac
                shift 2 ;;
            --database-url)
                [ $# -ge 2 ] || die "--database-url needs a value"
                DATABASE_URL_OPT="$2"
                shift 2 ;;
            --dry-run) DRY_RUN=1; shift ;;
            --purge) PURGE=1; shift ;;
            -h|--help) usage; exit 0 ;;
            *) die "unknown option: $1 (see: install.sh help)" ;;
        esac
    done
}

main() {
    local cmd="${1:-}"
    case "$cmd" in
        install) shift; parse_opts "$@"; do_install ;;
        upgrade) shift; parse_opts "$@"; do_upgrade ;;
        uninstall) shift; parse_opts "$@"; do_uninstall ;;
        status) do_status ;;
        help|-h|--help) usage ;;
        "")
            if [ -t 0 ]; then menu; else usage; exit 2; fi
            ;;
        *) usage >&2; die "unknown command: $cmd" ;;
    esac
}

main "$@"
