#!/usr/bin/env bash
# install.sh — one-click install / upgrade / uninstall for DHTSearch.
#
#   bash <(curl -fsSL https://raw.githubusercontent.com/cuijianzhuang/dhtsearch/main/scripts/install.sh) install
#
# Subcommands: install | upgrade | uninstall | status | start | stop |
# restart | check-update | backup | backups | restore | selfcheck |
# config | set-token | autostart | auto-update | upgrade-request
# Run with no arguments in a terminal for an interactive menu.
# install also copies this script to /usr/local/bin/dhtsearch, so on an
# installed machine plain `dhtsearch` re-opens the menu (install / upgrade /
# status / start / stop / restart / logs / change ports / switch database /
# autostart / config / backup & restore / selfcheck / check update /
# auto-update / uninstall) without re-downloading it.
#
# Auto-update: a systemd timer (dhtsearch-update.timer) can run
# `dhtsearch upgrade --yes` on a daily/weekly schedule. It is off by
# default; its state (enabled flag, frequency, last check) lives in
# /var/lib/dhtsearch/autoupdate — a script-owned state file, not the
# server env file, because it is installer state rather than server
# configuration and it has to survive with the data dir.
#
# Panel contract: the admin panel (which runs unprivileged and must
# never execute an upgrade itself) shares three more key=value files
# in the data dir with this script:
#   update-panel    written by the panel when an admin saves the
#                   AUTO_UPDATE_* settings; while it exists, its
#                   ENABLED/TIME override the CLI state above (the
#                   `auto-update` command takes control back by
#                   deleting it).
#   update-request  an upgrade request flag written by the panel's
#                   "upgrade now" button.
#   update-status   written by the upgrade agent: the outcome of the
#                   last panel-requested upgrade; while one runs it
#                   also carries PROGRESS / PHASE stage markers (see
#                   write_update_status).
#   restart-request a service-restart request flag written by the
#                   panel's "restart service" button (settings whose
#                   effect needs a restart can then be applied with
#                   no SSH session).
#   restart-status  written by the upgrade agent: the outcome of the
#                   last panel-requested restart (see
#                   write_restart_status).
# A per-minute timer (dhtsearch-upgrade-request.timer) runs the
# agent (`dhtsearch upgrade-request`): it reconciles the auto-update
# timer with the effective (panel-first) settings, consumes one
# pending restart request, and consumes one pending upgrade request,
# recording the results in restart-status / update-status.
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
#                              still point at the real paths). Test mode
#                              needs no root: the service user, the chown
#                              pass and systemctl are all skipped, and
#                              copies do not preserve ownership.
#   DHTSEARCH_SKIP_SYSTEMD=1   write unit files but never call systemctl
#   DHTSEARCH_DRY_RUN=1        same as --dry-run
set -euo pipefail

REPO="cuijianzhuang/dhtsearch"
SERVICE_USER="dhtsearch"
HEALTH_TIMEOUT=120

# Logical paths (also what the systemd units contain).
L_BIN="/usr/local/bin/dhtsearch-server"
L_LT_SIDECAR="/usr/local/bin/dhtsearch-lt-sidecar"
L_MANAGER="/usr/local/bin/dhtsearch"
L_WEB_DIR="/opt/dhtsearch/web"
L_CONF_DIR="/etc/dhtsearch"
L_ENV_FILE="/etc/dhtsearch/dhtsearch.env"
L_DATA_DIR="/var/lib/dhtsearch"

# Filesystem paths (prefixed under DHTSEARCH_ROOT when testing).
PREFIX="${DHTSEARCH_ROOT:-}"
BIN_FILE="${PREFIX}${L_BIN}"
LT_SIDECAR_FILE="${PREFIX}${L_LT_SIDECAR}"
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
UPDATE_UNIT="dhtsearch-update.service"
UPDATE_TIMER="dhtsearch-update.timer"
AUTOUPDATE_FILE="${DATA_DIR}/autoupdate"
REQUEST_UNIT="dhtsearch-upgrade-request.service"
REQUEST_TIMER="dhtsearch-upgrade-request.timer"
# Panel contract files (see the header comment): the panel writes
# update-panel / update-request / restart-request, the upgrade agent
# writes update-status / restart-status, and .au-applied stamps the
# auto-update configuration the agent last reconciled the systemd
# timer to.
UPDATE_PANEL_FILE="${DATA_DIR}/update-panel"
UPDATE_REQUEST_FILE="${DATA_DIR}/update-request"
UPDATE_STATUS_FILE="${DATA_DIR}/update-status"
RESTART_REQUEST_FILE="${DATA_DIR}/restart-request"
RESTART_STATUS_FILE="${DATA_DIR}/restart-status"
AU_APPLIED_FILE="${DATA_DIR}/.au-applied"

# Archive copies (package extraction, web tree, upgrade backups). In
# DHTSEARCH_ROOT test mode the tree belongs to whoever runs the test,
# while the release tarball's entries belong to the build user: asking
# tar/cp to preserve ownership there fails noisily ("failed to preserve
# ownership") and can abort the install halfway, so test-mode copies
# keep modes and timestamps but not ownership. A real install is
# unaffected — fix_permissions assigns the service user afterwards.
CP_A=(cp -a)
TAR_EXTRACT=(tar -xzf)
TAR_EXTRACT_PLAIN=(tar -xf)
if [ -n "$PREFIX" ]; then
    CP_A=(cp -a --no-preserve=ownership)
    TAR_EXTRACT=(tar --no-same-owner -xzf)
    TAR_EXTRACT_PLAIN=(tar --no-same-owner -xf)
fi

# Backup retention, enforced by rotate_backups after every upgrade and
# every database backup: upgrade snapshot sets (server binary, env and
# web tree sharing one timestamp) keep the newest KEEP_UPGRADE_SETS
# sets; standalone database backups (db-*) keep the newest
# KEEP_DB_BACKUPS files. Without this the backups dir grows by a full
# web tree (~40 MB) per upgrade, forever.
KEEP_UPGRADE_SETS=3
KEEP_DB_BACKUPS=10

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
# Set by menu(): the temp file the background latest-version lookup
# writes into (see menu_header); removed by the same EXIT cleanup.
MENU_LATEST_FILE=""
cleanup_tmp() {
    if [ -n "$CLEANUP_DIR" ]; then rm -rf "$CLEANUP_DIR"; fi
    if [ -n "$MENU_LATEST_FILE" ]; then rm -f "$MENU_LATEST_FILE"; fi
}
trap cleanup_tmp EXIT
FROM_SOURCE=0
LOCAL_FILE=""
PURGE=0
DRY_RUN="${DHTSEARCH_DRY_RUN:-0}"
YES_OPT=0
CHECK_OPT=0
# Set by install_package_files to the install.sh copy inside the
# package being installed, when the package carries one.
PKG_MANAGER_SRC=""

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
                       check fails. When the target version is the one
                       already installed, upgrade is a no-op: it reports
                       已是最新版本 and exits 0 without downloading,
                       backing up or restarting anything. A target older
                       than the installed version is refused (no
                       downgrades)
  uninstall            stop services and remove binary/web/units (and
                       the auto-update timer); config and data are kept
                       unless --purge is given
  status               show service state, installed version, health,
                       autostart and auto-update state
  start | stop | restart
                       control the API and web services
  check-update         compare the installed version with the latest
                       release without changing anything; the result is
                       recorded and shown by status. Exit code: 0 = up
                       to date, 2 = update available, 1 = check failed
  backup               back up the database now (SQLite: file copy;
                       PostgreSQL: pg_dump) into the backups dir
  backups              list database backups
  restore FILE         restore a database backup: stops the services
                       and snapshots the current database first
  selfcheck            run the health checklist: services, ports, disk,
                       database connectivity, recent error log summary
  config               print the config file with secrets masked
  set-token [VALUE]    set/reset ADMIN_TOKEN in the config file; with
                       no VALUE a random one is generated and printed
  autostart on|off|status
                       toggle boot-time start of the two services
  auto-update on [daily|weekly] | off | status
                       scheduled automatic upgrades via a systemd timer
                       (off by default). Upgrades keep the normal
                       backup + health-check + rollback behaviour.
                       State is kept in /var/lib/dhtsearch/autoupdate.
                       The admin panel's 系统更新 settings override
                       this state while its contract file
                       (/var/lib/dhtsearch/update-panel) exists; using
                       this command deletes that file and takes control
                       back.
  upgrade-request      the panel upgrade agent (run by
                       dhtsearch-upgrade-request.timer every minute):
                       reconciles the auto-update timer with the
                       panel/CLI settings and runs one panel-requested
                       upgrade, if any. Not usually run by hand.
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
  --check              (upgrade) only check for a newer release, like
                       check-update, without upgrading
  --yes                run non-interactively (no prompts); used by the
                       auto-update timer
  --dry-run            write config/unit files but do not touch systemd
                       or start services

Options (uninstall):
  --purge              also delete /etc/dhtsearch and /var/lib/dhtsearch

Environment:
  API_PORT / WEB_PORT    default ports when the matching --*-port flag
                         is not given (same priority rules as above)
  DHTSEARCH_ROOT         prefix all install paths (testing; the run
                         then needs no root and preserves no ownership)
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
    # DHTSEARCH_ROOT test mode (local testing / CI) never touches real
    # system state — every path is prefixed, the service user and the
    # chown pass are skipped, and systemctl is not called — so it must
    # not demand root; requiring it would defeat the hook.
    if [ -n "$PREFIX" ]; then return 0; fi
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

# gen_secret — 64 hex chars of randomness (same recipe as do_set_token).
gen_secret() {
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -hex 32
    else
        od -An -tx1 -N32 /dev/urandom | tr -d ' \n'
    fi
}

# ensure_internal_token — the first-party credential shared by the API
# and the web frontend (INTERNAL_API_TOKEN). The API reads it from the
# config file (via EnvironmentFile); write_units stamps the same value
# into the web unit, so the web's server-side calls stay exempt from
# PUBLIC_API_REQUIRE_TOKEN. Generated once, then kept: an existing
# value is never overwritten, so the two sides cannot drift apart on
# upgrade. The value itself is never printed.
ensure_internal_token() {
    [ -f "$ENV_FILE" ] || return 0
    if [ -z "$(env_value INTERNAL_API_TOKEN)" ]; then
        set_env_value INTERNAL_API_TOKEN "$(gen_secret)"
        log "Generated INTERNAL_API_TOKEN (web-to-API first-party channel) in $ENV_FILE; the web unit carries the same value."
    fi
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
    if ! "${TAR_EXTRACT[@]}" "$PKG_TARBALL" -C "$tmp" --wildcards --no-anchored 'dhtsearch-server' 2>/dev/null; then
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
        ensure_internal_token
        return
    fi
    local ap wp db_url_line internal_tok
    ap="$(resolve_api_port)"
    wp="$(resolve_web_port)"
    internal_tok="$(gen_secret)"
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

# First-party channel between the web frontend and the API: when
# PUBLIC_API_REQUIRE_TOKEN is on, the web presents this token so the
# site itself is not locked out by the token gate. install.sh writes
# the same value into the web systemd unit; if you edit it here, keep
# the two in sync (or delete it and re-run install to regenerate).
INTERNAL_API_TOKEN=${internal_tok}
EOF
    chmod 600 "$ENV_FILE"
}

write_units() {
    local node_bin ap wp internal_tok internal_line
    node_bin="$(command -v node)"
    ap="$(resolve_api_port)"
    wp="$(resolve_web_port)"
    # The web unit carries the first-party token so the frontend can
    # present it to the API; the API reads the same value from the
    # config file. ensure_* generates it first for pre-existing
    # installs that predate the channel.
    ensure_internal_token
    internal_tok="$(env_value INTERNAL_API_TOKEN)"
    internal_line=""
    if [ -n "$internal_tok" ]; then
        internal_line="Environment=INTERNAL_API_TOKEN=${internal_tok}"
    fi
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
${internal_line}
ExecStart=${node_bin} ${L_WEB_DIR}/server.js
# Node's standalone server answers SIGTERM by exiting with code 143
# (128+15) instead of dying by the signal; without this, a plain
# systemctl stop leaves the unit marked failed.
SuccessExitStatus=143
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

# version_is_release V — V is a plain vX.Y.Z release version (the
# only shape GitHub releases of this project ever carry). Anything
# else ("unknown", "local", "dev-<sha>") has no meaningful ordering.
version_is_release() {
    [[ "$1" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+$ ]]
}

# version_cmp A B — print -1, 0 or 1 as A is older than, equal to or
# newer than B. Fields are compared numerically (v0.1.9 < v0.1.10);
# a leading v is ignored and missing fields count as 0.
version_cmp() {
    local a="${1#v}" b="${2#v}" i x y
    for i in 1 2 3; do
        x="${a%%.*}"; y="${b%%.*}"
        case "$a" in *.*) a="${a#*.}" ;; *) a="" ;; esac
        case "$b" in *.*) b="${b#*.}" ;; *) b="" ;; esac
        x="${x%%[!0-9]*}"; y="${y%%[!0-9]*}"
        # Strip leading zeros so the -gt/-lt below stay decimal.
        x="${x#"${x%%[!0]*}"}"; y="${y#"${y%%[!0]*}"}"
        [ -n "$x" ] || x=0
        [ -n "$y" ] || y=0
        if [ "$x" -gt "$y" ]; then echo 1; return 0; fi
        if [ "$x" -lt "$y" ]; then echo -1; return 0; fi
    done
    echo 0
}

resolve_latest_version() {
    local tag
    tag="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
    [ -n "$tag" ] || die "could not resolve latest release from GitHub API; use --version to pin one"
    echo "$tag"
}

# resolve_latest_version_quiet — same lookup, but prints "" instead of
# dying when the network or the API is unavailable (check-update must
# be able to report a failed check, not crash on one).
resolve_latest_version_quiet() {
    local tag=""
    tag="$(curl -fsSL --max-time 20 "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null \
        | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)" || tag=""
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
    # Optional libtorrent sidecar asset: downloaded and verified by
    # fetch_sidecar_asset, which classifies the outcome into
    # LT_SIDECAR_STATE for the final summary instead of the old
    # best-effort block that silently continued on every failure —
    # that silence is how a host ended up with META_FETCHER=libtorrent
    # selected but no sidecar on disk and a service that would not
    # start. The install itself never fails over the sidecar.
    fetch_sidecar_asset "$version" "$arch" "$dest" "$base"
}

# fetch_sidecar_once <base-url> <asset-name> <dest-dir> — one attempt
# at downloading the sidecar asset and its .sha256 and verifying it.
# Returns 0 when the asset is on disk and sha256-verified, 1 when the
# release simply carries no such asset (HTTP 404 on the asset or on
# its checksum file — releases that predate the sidecar, or whose
# sidecar job failed), 2 on any other failure (network error, other
# HTTP status, checksum mismatch).
fetch_sidecar_once() {
    local base="$1" lt_name="$2" dest="$3"
    local code
    code="$(curl -sSL -o "${dest}/${lt_name}" -w '%{http_code}' "${base}/${lt_name}" 2>/dev/null)" || return 2
    [ "$code" = "404" ] && return 1
    [ "$code" = "200" ] || return 2
    code="$(curl -sSL -o "${dest}/${lt_name}.sha256" -w '%{http_code}' "${base}/${lt_name}.sha256" 2>/dev/null)" || return 2
    [ "$code" = "404" ] && return 1
    [ "$code" = "200" ] || return 2
    (cd "$dest" && sha256sum -c "${lt_name}.sha256" >/dev/null 2>&1) || return 2
    return 0
}

# fetch_sidecar_asset <version> <arch> <dest-dir> <base-url> — get
# the release's libtorrent sidecar asset into <dest-dir> and record
# the outcome in LT_SIDECAR_STATE / LT_SIDECAR_SRC. Never dies: the
# sidecar is an optional component (only META_FETCHER=libtorrent
# needs it) and its absence must not fail an install or upgrade. A
# download/checksum failure is retried once, then reported loudly;
# a 404 is not retried — the asset is simply not in this release.
fetch_sidecar_asset() {
    local version="$1" arch="$2" dest="$3" base="$4"
    local lt_name="dhtsearch-lt-sidecar-linux-${arch}"
    local attempt rc
    for attempt in 1 2; do
        rc=0
        fetch_sidecar_once "$base" "$lt_name" "$dest" || rc=$?
        if [ "$rc" = "0" ]; then
            LT_SIDECAR_SRC="${dest}/${lt_name}"
            log "Downloaded libtorrent sidecar asset ${lt_name} (sha256 OK)"
            return 0
        fi
        rm -f "${dest}/${lt_name}" "${dest}/${lt_name}.sha256"
        if [ "$rc" = "1" ]; then
            LT_SIDECAR_STATE="absent-in-release"
            log "Release ${version} carries no libtorrent sidecar asset (optional; META_FETCHER=libtorrent stays unavailable until a release ships one)"
            return 0
        fi
        [ "$attempt" = "1" ] && warn "libtorrent sidecar download failed; retrying once"
    done
    LT_SIDECAR_STATE="download-failed"
    warn "libtorrent sidecar asset could not be downloaded or verified after a retry; continuing without it (META_FETCHER=libtorrent will be unavailable)"
}

# lt_sidecar_line — the one-line sidecar state shown at the end of
# every install/upgrade, in `dhtsearch status`, and (via the captured
# upgrade log) in the panel's update-status message, so a missing
# sidecar is impossible to miss instead of a silent surprise the day
# someone selects the libtorrent foundation.
lt_sidecar_line() {
    if [ -f "$LT_SIDECAR_FILE" ]; then
        case "$LT_SIDECAR_STATE" in
            absent-in-release) printf 'libtorrent 边车：已安装 %s（此前已装；本次发布未附带边车资产，未更新）\n' "$LT_SIDECAR_FILE" ;;
            download-failed) printf 'libtorrent 边车：已安装 %s（此前已装；本次下载新版失败，未更新）\n' "$LT_SIDECAR_FILE" ;;
            *) printf 'libtorrent 边车：已安装 %s\n' "$LT_SIDECAR_FILE" ;;
        esac
    else
        case "$LT_SIDECAR_STATE" in
            absent-in-release) printf 'libtorrent 边车：未安装（该发布未附带边车资产，META_FETCHER=libtorrent 不可用）\n' ;;
            download-failed) printf 'libtorrent 边车：未安装（边车资产下载失败，META_FETCHER=libtorrent 不可用；重新运行升级可重试）\n' ;;
            *) printf 'libtorrent 边车：未安装（无边车文件，META_FETCHER=libtorrent 不可用；安装/升级时会自动下载发布资产）\n' ;;
        esac
    fi
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
    "${TAR_EXTRACT[@]}" "$tarball" -C "$extract_dir"
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

    # The package carries a copy of this script. Remember it so
    # install_manager can refresh the management entry from the
    # package even when the running script IS the old entry (the case
    # that used to fail with "are the same file"): the package copy is
    # a different file, so it can be renamed over the entry safely.
    if [ -f "$pkg_dir/install.sh" ] && [ -n "$CLEANUP_DIR" ]; then
        cp "$pkg_dir/install.sh" "${CLEANUP_DIR}/manager.sh"
        PKG_MANAGER_SRC="${CLEANUP_DIR}/manager.sh"
    fi

    install -m 0755 "$pkg_dir/dhtsearch-server" "$BIN_FILE"
    # The libtorrent sidecar lives next to the server binary, where
    # the server's discovery looks first. Prefer a copy bundled in
    # the package (source builds with sidecar/dist present), else the
    # separately downloaded release asset. Absent both, an existing
    # sidecar is left untouched.
    local lt_src=""
    if [ -f "$pkg_dir/dhtsearch-lt-sidecar" ]; then
        lt_src="$pkg_dir/dhtsearch-lt-sidecar"
    elif [ -n "$LT_SIDECAR_SRC" ] && [ -f "$LT_SIDECAR_SRC" ]; then
        lt_src="$LT_SIDECAR_SRC"
    fi
    if [ -n "$lt_src" ]; then
        install -m 0755 "$lt_src" "$LT_SIDECAR_FILE"
        LT_SIDECAR_STATE="installed"
        log "Installed libtorrent sidecar -> $LT_SIDECAR_FILE"
    elif [ -z "$LT_SIDECAR_STATE" ] && [ -f "$LT_SIDECAR_FILE" ]; then
        # Nothing new obtained and nothing recorded by the download
        # step (e.g. a --file package without a bundled copy): the
        # sidecar already on disk is what the summary reports.
        LT_SIDECAR_STATE="already-present"
    fi
    rm -rf "$WEB_DIR"
    mkdir -p "$APP_DIR"
    "${CP_A[@]}" "$pkg_dir/web" "$WEB_DIR"
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
# `dhtsearch` opens the interactive menu. Source preference: the copy
# inside the package being installed (PKG_MANAGER_SRC), then this
# running script ($0), then — when the script was piped in
# (bash <(curl ...)), so $0 is not a regular file — the published copy
# fetched from GitHub. This is the management script only; the service
# binary dhtsearch-server is a different file and is never touched here.
#
# Two self-overwrite traps are handled:
#   - When the only source IS the management entry (a menu-initiated
#     upgrade with no package copy at hand), copying it over itself
#     fails with "are the same file"; skip quietly instead — the entry
#     is by definition the script already running.
#   - Otherwise the new file is staged next to the entry and renamed
#     over it. Rename swaps the directory entry atomically, so a bash
#     process executing the old entry keeps reading its (now unlinked)
#     inode undisturbed; truncating it in place would corrupt the
#     running script.
install_manager() {
    local src=""
    if [ -n "$PKG_MANAGER_SRC" ] && [ -f "$PKG_MANAGER_SRC" ]; then
        src="$PKG_MANAGER_SRC"
    elif [ -f "$0" ]; then
        src="$0"
    fi
    local tmp="${MANAGER_FILE}.new.$$"
    if [ -n "$src" ]; then
        if [ -f "$MANAGER_FILE" ] && [ "$src" -ef "$MANAGER_FILE" ]; then
            log "Management entry is this script itself; already in place: $MANAGER_FILE"
            return 0
        fi
        if cp "$src" "$tmp" && chmod 755 "$tmp" && mv -f "$tmp" "$MANAGER_FILE"; then
            log "Management entry installed: run 'dhtsearch' for the menu"
        else
            rm -f "$tmp"
            warn "could not install the management entry to $MANAGER_FILE"
        fi
        return 0
    fi
    if curl -fsSL "https://raw.githubusercontent.com/${REPO}/main/scripts/install.sh" -o "$tmp" 2>/dev/null; then
        chmod 755 "$tmp"
        mv -f "$tmp" "$MANAGER_FILE"
        log "Management entry installed: run 'dhtsearch' for the menu"
    else
        rm -f "$tmp"
        warn "could not install the management entry ($MANAGER_FILE); download install.sh and re-run install to get it"
    fi
}

# FILE_VERSION — the version read out of a --file tarball (see
# validate_local_file); empty when the package name carries none.
FILE_VERSION=""

# validate_local_file — check the --file package before anything on
# the system is touched, and read the release version out of it: the
# package's top-level directory is dhtsearch-<version>-linux-<arch>,
# so a local install records the real version instead of "local". A
# tarball that does not follow the layout still installs (the layout
# checks in install_package_files apply); it just keeps "local".
validate_local_file() {
    [ -n "$LOCAL_FILE" ] || return 0
    [ -f "$LOCAL_FILE" ] || die "--file not found: $LOCAL_FILE"
    local listing top v
    listing="$(tar -tzf "$LOCAL_FILE" 2>/dev/null)" \
        || die "--file is not a readable .tar.gz package: $LOCAL_FILE"
    top="${listing%%$'\n'*}"
    top="${top%%/*}"
    case "$top" in
        dhtsearch-*-linux-*)
            v="${top#dhtsearch-}"
            FILE_VERSION="${v%-linux-*}"
            ;;
    esac
}

# validate_inputs — every parameter and package check that can be done
# up front, run by install/upgrade before any system state changes
# (no directories, no service user, no downloads yet). The full
# database resolution still happens later in resolve_database; this is
# its cheap parameter-shape half, mirrored so a malformed --db /
# --database-url fails here instead of after the system was touched.
validate_inputs() {
    validate_local_file
    if [ "$FROM_SOURCE" -eq 1 ]; then
        local tool
        for tool in git go npm node; do
            command -v "$tool" >/dev/null 2>&1 || die "--from-source needs $tool on this machine"
        done
    fi
    local db="$DB_OPT"
    if [ -n "$DATABASE_URL_OPT" ] && [ -z "$db" ]; then db="postgres-url"; fi
    if [ "$db" = "postgres-url" ]; then
        [ -n "$DATABASE_URL_OPT" ] || die "--db postgres-url needs --database-url"
        valid_pg_url "$DATABASE_URL_OPT" \
            || die "invalid --database-url: must start with postgres:// or postgresql://"
    fi
}

# obtain_package <arch> <workdir>; sets PKG_TARBALL and RESOLVED_VERSION.
PKG_TARBALL=""
# Set by download_package when the release carries the optional
# libtorrent sidecar asset (verified against its .sha256).
LT_SIDECAR_SRC=""
# Outcome of the latest libtorrent sidecar acquisition, rendered by
# lt_sidecar_line in the install/upgrade summary, in status output
# and in the panel's update-status message:
#   installed          a sidecar copy was installed by this run
#   already-present    nothing new obtained, but a copy was on disk
#   absent-in-release  the release carries no sidecar asset (404)
#   download-failed    the asset exists but download/checksum failed
#   ""                 not attempted (--file / --from-source package)
LT_SIDECAR_STATE=""
RESOLVED_VERSION=""
obtain_package() {
    local arch="$1" workdir="$2"
    if [ -n "$LOCAL_FILE" ]; then
        [ -f "$LOCAL_FILE" ] || die "--file not found: $LOCAL_FILE"
        RESOLVED_VERSION="${VERSION:-${FILE_VERSION:-local}}"
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

# ------------------------------------------------------------- auto-update
# State lives in $AUTOUPDATE_FILE (/var/lib/dhtsearch/autoupdate):
#   ENABLED=0|1
#   FREQ=daily|weekly
#   LAST_CHECK=YYYY-MM-DD HH:MM:SS
#   LAST_RESULT=<free text: up-to-date vX / update available: vY / ...>
# A separate state file (not the server env file) on purpose: this is
# installer state, not server configuration, and the env file is the
# server's 600 root-only config. The timer simply runs the normal
# upgrade path (`dhtsearch upgrade --yes`), so backups, the health
# check and rollback on failure all apply unchanged.

# au_value KEY — value from the auto-update state file ("" if unset).
au_value() {
    local line=""
    if [ -f "$AUTOUPDATE_FILE" ]; then
        line="$(grep -E "^$1=" "$AUTOUPDATE_FILE" | tail -1 || true)"
    fi
    echo "${line#"$1"=}"
}

# panel_value KEY — value from the panel contract file ("" if unset).
panel_value() {
    local line=""
    if [ -f "$UPDATE_PANEL_FILE" ]; then
        line="$(grep -E "^$1=" "$UPDATE_PANEL_FILE" | tail -1 || true)"
    fi
    echo "${line#"$1"=}"
}

# panel_configured — the admin panel has taken over the auto-update
# switch: an admin saved the AUTO_UPDATE_* settings at least once and
# the panel synced them to $UPDATE_PANEL_FILE. While it exists, its
# ENABLED/TIME win over the CLI state file; au_set (the CLI) deletes
# it to take control back. FREQ always comes from the CLI state.
panel_configured() { [ -f "$UPDATE_PANEL_FILE" ]; }

au_enabled() {
    if panel_configured; then
        [ "$(panel_value ENABLED)" = "1" ]
    else
        [ "$(au_value ENABLED)" = "1" ]
    fi
}

# au_time — the daily run time (HH:MM) for the auto-update timer:
# the panel's TIME when the panel is in control, else the historical
# built-in 03:37. A malformed panel value falls back to 03:37 rather
# than producing a broken OnCalendar.
au_time() {
    local t=""
    if panel_configured; then
        t="$(panel_value TIME)"
    fi
    if [[ "$t" =~ ^([01][0-9]|2[0-3]):[0-5][0-9]$ ]]; then
        echo "$t"
    else
        echo "03:37"
    fi
}

au_freq() {
    local f
    f="$(au_value FREQ)"
    echo "${f:-daily}"
}

# au_write ENABLED FREQ — rewrite the state file, keeping last-check.
au_write() {
    local lc lr
    lc="$(au_value LAST_CHECK)"
    lr="$(au_value LAST_RESULT)"
    mkdir -p "$DATA_DIR"
    {
        printf 'ENABLED=%s\n' "$1"
        printf 'FREQ=%s\n' "$2"
        printf 'LAST_CHECK=%s\n' "$lc"
        printf 'LAST_RESULT=%s\n' "$lr"
    } > "$AUTOUPDATE_FILE"
}

# au_record RESULT — stamp the last check time and its outcome.
au_record() {
    local en=0 freq lc
    au_enabled && en=1
    freq="$(au_freq)"
    lc="$(date '+%Y-%m-%d %H:%M:%S')"
    mkdir -p "$DATA_DIR"
    {
        printf 'ENABLED=%s\n' "$en"
        printf 'FREQ=%s\n' "$freq"
        printf 'LAST_CHECK=%s\n' "$lc"
        printf 'LAST_RESULT=%s\n' "$1"
    } > "$AUTOUPDATE_FILE"
}

write_update_units() {
    local oncal
    case "$(au_freq)" in
        weekly) oncal="Mon *-*-* $(au_time):00" ;;
        *) oncal="*-*-* $(au_time):00" ;;
    esac
    cat > "${UNIT_DIR}/${UPDATE_UNIT}" <<EOF
[Unit]
Description=DHTSearch automatic update (upgrade to the latest release)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
Environment=DHTSEARCH_AUTO_UPDATE=1
ExecStart=${L_MANAGER} upgrade --yes
EOF
    cat > "${UNIT_DIR}/${UPDATE_TIMER}" <<EOF
[Unit]
Description=Scheduled DHTSearch automatic update

[Timer]
OnCalendar=${oncal}
RandomizedDelaySec=1800
Persistent=true
Unit=${UPDATE_UNIT}

[Install]
WantedBy=timers.target
EOF
}

# au_apply — install/upgrade regenerate the unit dir; when auto-update
# is on, put its units back and re-arm the timer so the schedule
# survives the upgrade (including the timer's own upgrades). Also
# stamps $AU_APPLIED_FILE so the upgrade agent's reconcile does not
# redo this work on its next tick.
au_apply() {
    if ! au_enabled; then
        printf '0\n' > "$AU_APPLIED_FILE" 2>/dev/null || true
        return 0
    fi
    write_update_units
    svc daemon-reload
    if have_systemd; then
        systemctl enable --now "$UPDATE_TIMER" >/dev/null 2>&1 || true
    fi
    printf '1 %s %s\n' "$(au_time)" "$(au_freq)" > "$AU_APPLIED_FILE"
}

au_status_line() {
    local src="cli"
    panel_configured && src="panel"
    if au_enabled; then
        local t="timer inactive"
        if have_systemd && systemctl is-active --quiet "$UPDATE_TIMER" 2>/dev/null; then
            t="timer active"
        fi
        echo "enabled ($(au_freq) $(au_time), source: $src, $t)"
    else
        echo "disabled (source: $src)"
    fi
}

# au_set on [daily|weekly] | off — the auto-update command behind both
# the menu entry and the auto-update subcommand.
au_set() {
    local mode="$1" freq="${2:-}"
    # The CLI is an explicit operator action: it takes control back
    # from the admin panel by dropping the panel contract file, so
    # the two never fight over the switch.
    if panel_configured; then
        rm -f "$UPDATE_PANEL_FILE"
        log "Removed the panel's update settings ($UPDATE_PANEL_FILE); the CLI state is authoritative again"
    fi
    case "$mode" in
        on)
            [ -n "$freq" ] || freq="$(au_freq)"
            case "$freq" in
                daily|weekly) ;;
                *) die "invalid auto-update frequency: '$freq' (want daily | weekly)" ;;
            esac
            mkdir -p "$DATA_DIR" "$UNIT_DIR"
            au_write 1 "$freq"
            # The timer executes the management entry; make sure it
            # exists (it normally does — install put it there).
            if [ ! -f "$MANAGER_FILE" ]; then
                install_manager
            fi
            [ -f "$MANAGER_FILE" ] || die "management entry $MANAGER_FILE is missing; re-run install first, the auto-update timer needs it"
            write_update_units
            svc daemon-reload
            if have_systemd; then
                systemctl enable --now "$UPDATE_TIMER" >/dev/null 2>&1 \
                    || warn "could not enable $UPDATE_TIMER; check: systemctl status $UPDATE_TIMER"
            fi
            printf '1 %s %s\n' "$(au_time)" "$freq" > "$AU_APPLIED_FILE"
            log "Auto-update enabled ($freq at $(au_time), with a random delay of up to 30m): $UPDATE_TIMER"
            ;;
        off)
            au_write 0 "$(au_freq)"
            if have_systemd; then
                systemctl disable --now "$UPDATE_TIMER" >/dev/null 2>&1 || true
            fi
            rm -f "${UNIT_DIR}/${UPDATE_TIMER}" "${UNIT_DIR}/${UPDATE_UNIT}"
            svc daemon-reload
            printf '0\n' > "$AU_APPLIED_FILE"
            log "Auto-update disabled"
            ;;
        *) die "usage: auto-update on [daily|weekly] | off | status" ;;
    esac
}

# do_check_update — compare the installed version with the latest
# release, changing nothing. The outcome is recorded in the
# auto-update state file and shown by status. Returns 0 when up to
# date, 2 when an update is available, 1 when the check itself failed.
do_check_update() {
    local cur latest
    cur="$(installed_version)"
    echo "当前版本：$cur"
    latest="$(resolve_latest_version_quiet)"
    if [ -z "$latest" ]; then
        echo "最新版本：查询失败（网络不可用或 GitHub API 无响应）"
        au_record "check failed (network or API error)"
        return 1
    fi
    echo "最新版本：$latest"
    if [ "$cur" = "$latest" ]; then
        echo "已是最新版本。"
        au_record "up-to-date $latest"
        return 0
    fi
    echo "有新版本可升级：$cur -> $latest（运行 upgrade，或在 dhtsearch 菜单里选「升级」）"
    au_record "update available: $latest"
    return 2
}

# ------------------------------------------------------- upgrade agent
# The agent is a per-minute oneshot (dhtsearch-upgrade-request.timer)
# that bridges the admin panel and this script. The panel process is
# unprivileged and never runs an upgrade; it only writes intent into
# the data dir, and the agent — running as root — acts on it:
#
#   1. reconcile: keep the auto-update timer in sync with the
#      effective (panel-first) AUTO_UPDATE state, so a panel change
#      of the switch or the run time rebuilds/enables/disables the
#      timer within about a minute. $AU_APPLIED_FILE stamps what was
#      last applied; a matching stamp means no work (and no
#      daemon-reload) on this tick.
#   2. restart requests: consume $RESTART_REQUEST_FILE exactly once
#      — restart the API service and record the outcome in
#      $RESTART_STATUS_FILE (do_restart_request). Restarts run
#      before upgrades: they take seconds, and an admin who asked
#      for one is usually waiting to see a setting take effect.
#   3. upgrade requests: consume $UPDATE_REQUEST_FILE exactly once —
#      run the normal upgrade path and record the outcome in
#      $UPDATE_STATUS_FILE for the panel to display. A status file
#      already carrying the request's id means a previous tick
#      handled it; the flag is then just dropped.

write_request_units() {
    cat > "${UNIT_DIR}/${REQUEST_UNIT}" <<EOF
[Unit]
Description=DHTSearch upgrade agent (panel auto-update reconcile + on-demand upgrade/restart requests)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=${L_MANAGER} upgrade-request
EOF
    cat > "${UNIT_DIR}/${REQUEST_TIMER}" <<EOF
[Unit]
Description=Run the DHTSearch upgrade agent every minute

[Timer]
OnCalendar=*:*
Unit=${REQUEST_UNIT}

[Install]
WantedBy=timers.target
EOF
}

# request_apply — (re)install the agent's units and arm its timer.
# Called on install and upgrade, unconditionally: the agent is what
# makes panel-driven update settings work at all.
request_apply() {
    mkdir -p "$UNIT_DIR"
    write_request_units
    svc daemon-reload
    if have_systemd; then
        systemctl enable --now "$REQUEST_TIMER" >/dev/null 2>&1 || true
    fi
}

au_reconcile() {
    local want="0" applied=""
    if au_enabled; then
        want="1 $(au_time) $(au_freq)"
    fi
    if [ -f "$AU_APPLIED_FILE" ]; then
        applied="$(cat "$AU_APPLIED_FILE")"
    fi
    [ "$want" = "$applied" ] && return 0
    if au_enabled; then
        mkdir -p "$DATA_DIR" "$UNIT_DIR"
        [ -f "$MANAGER_FILE" ] || install_manager
        write_update_units
        svc daemon-reload
        if have_systemd; then
            systemctl enable --now "$UPDATE_TIMER" >/dev/null 2>&1 \
                || warn "could not enable $UPDATE_TIMER; check: systemctl status $UPDATE_TIMER"
        fi
        log "Auto-update timer applied from $(panel_configured && echo panel || echo cli) settings: $(au_freq) at $(au_time)"
    else
        if have_systemd; then
            systemctl disable --now "$UPDATE_TIMER" >/dev/null 2>&1 || true
        fi
        rm -f "${UNIT_DIR}/${UPDATE_TIMER}" "${UNIT_DIR}/${UPDATE_UNIT}"
        svc daemon-reload
        log "Auto-update timer removed (disabled)"
    fi
    printf '%s\n' "$want" > "$AU_APPLIED_FILE"
}

# kv_value FILE KEY — KEY's value from a panel contract file.
kv_value() {
    local line=""
    if [ -f "$1" ]; then
        line="$(grep -E "^$2=" "$1" | tail -1 || true)"
    fi
    echo "${line#"$2"=}"
}

# write_update_status STATE REQUEST_ID REQUESTED_AT STARTED_AT
# FINISHED_AT FROM_VERSION TO_VERSION MESSAGE [PROGRESS PHASE] —
# atomically replace the agent's outcome file the panel polls.
# PROGRESS (0-100) and PHASE (a short stage label) are optional: they
# are written only when non-empty, so status files from older scripts
# (and outcomes where progress does not apply, like the already-latest
# no-op) simply carry no such lines and the panel falls back to the
# state text alone. The numbers mark real stage boundaries inside
# do_upgrade — stages completed, never a time estimate.
write_update_status() {
    mkdir -p "$DATA_DIR"
    local tmp="${UPDATE_STATUS_FILE}.tmp.$$"
    {
        printf 'STATE=%s\n' "$1"
        printf 'REQUEST_ID=%s\n' "$2"
        printf 'REQUESTED_AT=%s\n' "$3"
        printf 'STARTED_AT=%s\n' "$4"
        printf 'FINISHED_AT=%s\n' "$5"
        printf 'FROM_VERSION=%s\n' "$6"
        printf 'TO_VERSION=%s\n' "$7"
        printf 'MESSAGE=%s\n' "$(printf '%s' "$8" | tr '\n' ' ')"
        [ -n "${9:-}" ] && printf 'PROGRESS=%s\n' "$9"
        [ -n "${10:-}" ] && printf 'PHASE=%s\n' "${10}"
    } > "$tmp"
    mv "$tmp" "$UPDATE_STATUS_FILE"
}

# write_restart_status STATE REQUEST_ID REQUESTED_AT REQUESTED_BY
# STARTED_AT FINISHED_AT MESSAGE — atomically replace the restart
# outcome file the panel polls (the restart counterpart of
# write_update_status, minus the version/progress fields a restart
# does not have).
write_restart_status() {
    mkdir -p "$DATA_DIR"
    local tmp="${RESTART_STATUS_FILE}.tmp.$$"
    {
        printf 'STATE=%s\n' "$1"
        printf 'REQUEST_ID=%s\n' "$2"
        printf 'REQUESTED_AT=%s\n' "$3"
        printf 'REQUESTED_BY=%s\n' "$4"
        printf 'STARTED_AT=%s\n' "$5"
        printf 'FINISHED_AT=%s\n' "$6"
        printf 'MESSAGE=%s\n' "$(printf '%s' "$7" | tr '\n' ' ')"
    } > "$tmp"
    mv "$tmp" "$RESTART_STATUS_FILE"
}

# wait_api_health — poll the API's health endpoint after a restart
# (up to 60s). Only the API is checked: a panel-requested restart
# restarts dhtsearch.service alone, never the web unit. Skipped —
# successfully — in dry-run / no-systemd mode, like wait_health.
wait_api_health() {
    if [ "$DRY_RUN" = "1" ] || [ "${DHTSEARCH_SKIP_SYSTEMD:-0}" = "1" ]; then
        log "(health wait skipped in dry-run / no-systemd mode)"
        return 0
    fi
    local port deadline
    port="$(resolve_api_port)"
    deadline=$(( $(date +%s) + 60 ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        if curl -fsS "http://127.0.0.1:${port}/api/healthz" >/dev/null 2>&1; then
            return 0
        fi
        sleep 2
    done
    return 1
}

# do_restart_request — consume one panel restart request: restart
# dhtsearch.service (the API/crawler process — the one restart-
# required settings live in; the web service keeps serving the
# admin UI, so the panel stays up to watch) and record the outcome
# in $RESTART_STATUS_FILE. Runs under do_upgrade_request's lock, on
# the same per-minute agent tick as upgrade requests; no new units.
do_restart_request() {
    [ -f "$RESTART_REQUEST_FILE" ] || return 0
    local rid rat rby
    rid="$(kv_value "$RESTART_REQUEST_FILE" REQUEST_ID)"
    rat="$(kv_value "$RESTART_REQUEST_FILE" REQUESTED_AT)"
    rby="$(kv_value "$RESTART_REQUEST_FILE" REQUESTED_BY)"
    if [ -n "$rid" ] && [ "$(kv_value "$RESTART_STATUS_FILE" REQUEST_ID)" = "$rid" ]; then
        # Already handled by an earlier tick (its status is the
        # record); just drop the flag so it is not re-run.
        rm -f "$RESTART_REQUEST_FILE"
        return 0
    fi

    local started finished rc=0
    started="$(date '+%Y-%m-%d %H:%M:%S')"
    write_restart_status "running" "$rid" "$rat" "$rby" "$started" "" \
        "restart requested from the admin panel${rby:+ by $rby}; restarting $API_UNIT"
    log "restart-request: restarting $API_UNIT (request $rid, by ${rby:-unknown})"
    # Give the panel's HTTP response a head start: it was written
    # moments ago, but the browser may still be switching to its
    # "restarting" state, and the restart drops the API for a few
    # seconds — a poll racing the drop should read as "restarting",
    # never as a fresh error.
    sleep 3
    svc restart "$API_UNIT" || rc=$?
    finished="$(date '+%Y-%m-%d %H:%M:%S')"
    if [ "$rc" = "0" ] && wait_api_health; then
        write_restart_status "succeeded" "$rid" "$rat" "$rby" "$started" "$finished" \
            "service restarted; API is answering again"
        log "restart-request: $API_UNIT restarted and healthy (request $rid)"
    else
        write_restart_status "failed" "$rid" "$rat" "$rby" "$started" "$finished" \
            "restart failed or the API did not come back; check: journalctl -u $API_UNIT -n 100"
        warn "restart-request: $API_UNIT did not come back healthy (request $rid, restart exit $rc)"
    fi
    rm -f "$RESTART_REQUEST_FILE"
    return 0
}

# update_progress PROGRESS PHASE — record a mid-upgrade stage in the
# status file so the panel can draw a progress bar. Only acts when
# do_upgrade runs under do_upgrade_request, the one context that owns
# $UPDATE_STATUS_FILE: that function sets the UPG_* globals with the
# request's identity before invoking do_upgrade (the subshell that
# runs it inherits them), and this helper is a no-op without them.
# Plain CLI / menu / auto-update runs
# therefore never clobber a stale status file. The request's MESSAGE
# is preserved; STATE stays "running" until do_upgrade_request writes
# the outcome.
update_progress() {
    [ -n "${UPG_REQUEST_ID:-}" ] || return 0
    local msg
    msg="$(kv_value "$UPDATE_STATUS_FILE" MESSAGE)"
    write_update_status "running" "$UPG_REQUEST_ID" "${UPG_REQUESTED_AT:-}" \
        "${UPG_STARTED_AT:-}" "" "${UPG_FROM_VERSION:-}" "" \
        "${msg:-upgrade running}" "$1" "$2"
}

do_upgrade_request() {
    mkdir -p "$DATA_DIR"
    # Serialize ticks (and manual runs): two agents must never drive
    # upgrades concurrently. systemd already serializes the oneshot
    # unit; the flock covers manual invocations racing the timer.
    exec 9>"${DATA_DIR}/.upgrade-agent.lock"
    if command -v flock >/dev/null 2>&1; then
        flock -n 9 || exit 0
    fi

    au_reconcile

    # Restarts before upgrades (see the agent comment above): quick,
    # and the admin who requested one is usually watching.
    do_restart_request

    [ -f "$UPDATE_REQUEST_FILE" ] || return 0
    local rid rat rby
    rid="$(kv_value "$UPDATE_REQUEST_FILE" REQUEST_ID)"
    rat="$(kv_value "$UPDATE_REQUEST_FILE" REQUESTED_AT)"
    rby="$(kv_value "$UPDATE_REQUEST_FILE" REQUESTED_BY)"
    if [ -n "$rid" ] && [ "$(kv_value "$UPDATE_STATUS_FILE" REQUEST_ID)" = "$rid" ]; then
        # Already handled by an earlier tick (its status is the
        # record); just drop the flag so it is not re-run.
        rm -f "$UPDATE_REQUEST_FILE"
        return 0
    fi

    local from to started finished rc=0 logf tail_msg last_progress last_phase lt_note
    from="$(installed_version)"
    started="$(date '+%Y-%m-%d %H:%M:%S')"
    logf="$(mktemp)"
    write_update_status "running" "$rid" "$rat" "$started" "" "$from" "" \
        "upgrade requested from the admin panel${rby:+ by $rby}; running" "2" "准备"
    log "upgrade-request: running panel-requested upgrade (request $rid, by ${rby:-unknown}) from $from; output captured to the journal"
    # Progress context for do_upgrade's update_progress calls: the
    # subshell below inherits these globals, which is how do_upgrade
    # knows this run owns the status file (see update_progress).
    UPG_REQUEST_ID="$rid"
    UPG_REQUESTED_AT="$rat"
    UPG_STARTED_AT="$started"
    UPG_FROM_VERSION="$from"
    ( parse_opts --yes; do_upgrade ) >>"$logf" 2>&1 || rc=$?
    UPG_REQUEST_ID=""
    finished="$(date '+%Y-%m-%d %H:%M:%S')"
    to="$(installed_version)"
    if [ "$rc" = "0" ]; then
        if grep -q "无需升级" "$logf"; then
            # The version gate found nothing to do: a completed
            # no-op, reported as success — never as a failure and
            # never left looking like a pending request. No progress
            # fields: the run never entered the upgrade stages, so
            # the panel shows the message alone, not a bar.
            write_update_status "succeeded" "$rid" "$rat" "$started" "$finished" "$from" "$to" \
                "已是最新版本 $to，无需升级"
            log "upgrade-request: already up to date ($to); nothing to do"
        else
            # Carry the sidecar outcome into the panel-visible
            # message: do_upgrade ran in a subshell, so its
            # LT_SIDECAR_STATE is gone here, but its summary line
            # (lt_sidecar_line) is in the captured log.
            lt_note="$(grep '^==> libtorrent 边车' "$logf" | tail -1 | sed 's/^==> //' || true)"
            write_update_status "succeeded" "$rid" "$rat" "$started" "$finished" "$from" "$to" \
                "upgraded $from -> $to${lt_note:+; $lt_note}" "100" "完成"
            log "upgrade-request: succeeded ($from -> $to)"
        fi
    else
        tail_msg="$(tail -n 3 "$logf" | tr '\n' ' ')"
        # Keep the last stage do_upgrade reached: the bar freezes
        # where the work actually stopped, next to the error text.
        last_progress="$(kv_value "$UPDATE_STATUS_FILE" PROGRESS)"
        last_phase="$(kv_value "$UPDATE_STATUS_FILE" PHASE)"
        write_update_status "failed" "$rid" "$rat" "$started" "$finished" "$from" "$to" \
            "upgrade failed (exit $rc): $tail_msg" "$last_progress" "$last_phase"
        warn "upgrade-request: failed (exit $rc); last output: $tail_msg"
    fi
    cat "$logf" 2>/dev/null || true
    rm -f "$UPDATE_REQUEST_FILE" "$logf"
    return 0
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

    # All parameter and package validation happens before any system
    # state changes: a bad --file / --database-url / --from-source
    # environment fails here, not after the service user and the
    # directory tree have been created.
    validate_inputs
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
    prepare_dirs_and_user
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
    au_apply
    request_apply
    log "Installed DHTSearch $RESOLVED_VERSION (arch $arch)"
    log "API: http://127.0.0.1:$(resolve_api_port)/api/healthz   Web: http://127.0.0.1:$(resolve_web_port)/"
    log "Database: $(db_display)"
    if [ "$LOCAL_PG_PASSWORD_NEW" = "1" ]; then
        log "Local PostgreSQL configured. Connection URL, shown only this once — save it:"
        log "  $RESOLVED_DATABASE_URL"
        log "  (it is also stored in $ENV_FILE, readable by root only)"
    fi
    log "Config: $ENV_FILE   Data: $DATA_DIR   Logs: journalctl -u $API_UNIT -f"
    log "$(lt_sidecar_line)"
    if [ "$LT_SIDECAR_STATE" = "download-failed" ]; then
        warn "libtorrent 边车本次未能就位（下载失败）：不影响本次安装与其余底座；网络恢复后重新运行升级会自动重试下载"
    fi
    log "Manage: run 'dhtsearch' for the interactive menu"
}

# upgrade_version_gate — decide, before anything is downloaded,
# backed up or restarted, whether this upgrade has anything to do.
# A no-change upgrade otherwise restarts both services for nothing,
# and a stop that gets interrupted mid-upgrade has left both services
# down before — pointless restarts are a real risk, not a nit.
#
# Returns 0 to proceed with the upgrade, 1 when the target is the
# version already installed (the caller then returns success, having
# touched nothing). Dies when the target is OLDER than the installed
# version (upgrades never downgrade; there is deliberately no force
# flag — reinstall via uninstall + install instead) or when the
# latest version cannot be resolved at all.
#
# Target resolution mirrors obtain_package: --version wins, then the
# version parsed out of a --file tarball's name, then the latest
# release (via the same quiet lookup check-update uses — no second
# network path). --from-source builds carry no comparable version,
# and installs recorded as "unknown" / "local" / "dev-*" have no
# ordering either; both bypass the gate with a note.
upgrade_version_gate() {
    local cur target cmp
    cur="$(installed_version)"
    if [ "$FROM_SOURCE" -eq 1 ]; then
        echo "提示：--from-source 构建没有可比较的版本号，继续执行升级流程。"
        return 0
    fi
    if [ -n "$LOCAL_FILE" ]; then
        # Sets FILE_VERSION; dies on an unreadable package, exactly
        # as the validate_inputs call later in do_upgrade would.
        validate_local_file
        target="${VERSION:-$FILE_VERSION}"
    elif [ -n "$VERSION" ]; then
        target="$VERSION"
    else
        target="$(resolve_latest_version_quiet)"
        [ -n "$target" ] || die "无法查询最新版本（网络不可用或 GitHub API 无响应），已停止升级；请稍后重试，或用 --version 指定目标版本"
    fi
    [ -n "$target" ] && target="$(normalize_version "$target")"
    if ! version_is_release "$cur" || ! version_is_release "$target"; then
        echo "提示：当前版本 ${cur} / 目标版本 ${target:-未知} 不是标准版本号，无法比较，继续执行升级流程。"
        return 0
    fi
    cmp="$(version_cmp "$target" "$cur")"
    if [ "$cmp" = "0" ]; then
        echo "已是最新版本 $target，无需升级。"
        return 1
    fi
    if [ "$cmp" = "-1" ]; then
        die "目标版本 $target 低于当前版本 $cur，已停止：升级不会降级（先 uninstall 再 install 可装回旧版本）"
    fi
    log "升级：$cur -> $target"
    return 0
}

do_upgrade() {
    require_root
    if [ "$CHECK_OPT" = "1" ]; then
        # upgrade --check: report only, exactly like check-update.
        local check_rc=0
        do_check_update || check_rc=$?
        return "$check_rc"
    fi
    [ -f "$BIN_FILE" ] || die "not installed yet; run install first"
    if [ -n "$DB_OPT" ]; then
        warn "--db is ignored by upgrade: the database backend is never changed here. Use the dhtsearch menu (数据库切换) to switch, which also migrates the data."
        DB_OPT=""
    fi
    # Version gate: same-or-older target means no download, no
    # backup, no service restart — return success right here.
    if ! upgrade_version_gate; then
        if [ "${DHTSEARCH_AUTO_UPDATE:-0}" = "1" ]; then
            au_record "up-to-date $(installed_version)"
        fi
        return 0
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

    validate_inputs
    update_progress 10 "下载更新"
    obtain_package "$arch" "$workdir"
    tarball="$PKG_TARBALL"
    update_progress 30 "校验"

    prepare_dirs_and_user
    log "Backing up current install ($old_version) to $BACKUP_DIR ($ts)"
    local bk_start web_snap=""
    bk_start="$(epoch_now)"
    "${CP_A[@]}" "$BIN_FILE" "${BACKUP_DIR}/dhtsearch-server.${ts}.bak"
    [ -f "$LT_SIDECAR_FILE" ] && "${CP_A[@]}" "$LT_SIDECAR_FILE" "${BACKUP_DIR}/dhtsearch-lt-sidecar.${ts}.bak"
    [ -f "$ENV_FILE" ] && "${CP_A[@]}" "$ENV_FILE" "${BACKUP_DIR}/dhtsearch.env.${ts}.bak"
    if [ -d "$WEB_DIR" ]; then
        web_snap="$(snapshot_tree "$WEB_DIR" "${BACKUP_DIR}/web.${ts}.bak")"
        log "Web tree snapshot: $web_snap"
    fi
    log "Backup done: install snapshot $ts ($(elapsed_since "$bk_start")s)"
    update_progress 50 "备份完成"

    install_package_files "$tarball"
    write_env_file
    write_units
    fix_permissions
    update_progress 70 "安装"
    restart_services
    update_progress 85 "重启服务"
    if wait_health; then
        update_progress 100 "完成"
        record_version "$RESOLVED_VERSION"
        install_manager
        if [ "${DHTSEARCH_AUTO_UPDATE:-0}" = "1" ]; then
            au_record "auto-upgraded: $old_version -> $RESOLVED_VERSION"
        fi
        au_apply
        request_apply
        log "Upgraded: $old_version -> $RESOLVED_VERSION"
        log "$(lt_sidecar_line)"
        if [ "$LT_SIDECAR_STATE" = "download-failed" ]; then
            warn "libtorrent 边车本次未能就位（下载失败）：不影响本次升级与其余底座；网络恢复后重新运行升级会自动重试下载"
        fi
        rotate_backups "$ts"
        return 0
    fi

    warn "New version not healthy; rolling back to $old_version"
    install -m 0755 "${BACKUP_DIR}/dhtsearch-server.${ts}.bak" "$BIN_FILE"
    if [ -f "${BACKUP_DIR}/dhtsearch-lt-sidecar.${ts}.bak" ]; then
        install -m 0755 "${BACKUP_DIR}/dhtsearch-lt-sidecar.${ts}.bak" "$LT_SIDECAR_FILE"
    fi
    if [ -n "$web_snap" ]; then
        restore_tree "$web_snap" "$WEB_DIR"
    fi
    restart_services
    rotate_backups "$ts"
    if wait_health; then
        die "upgrade failed and was rolled back to $old_version; backup kept in $BACKUP_DIR"
    fi
    die "upgrade failed AND rollback is not healthy; backup in $BACKUP_DIR, logs: journalctl -u $API_UNIT -n 100"
}

do_uninstall() {
    require_root
    if have_systemd; then
        systemctl disable --now "$API_UNIT" "$WEB_UNIT" >/dev/null 2>&1 || true
        systemctl disable --now "$UPDATE_TIMER" >/dev/null 2>&1 || true
        systemctl disable --now "$REQUEST_TIMER" >/dev/null 2>&1 || true
    fi
    rm -f "${UNIT_DIR}/${API_UNIT}" "${UNIT_DIR}/${WEB_UNIT}" "${UNIT_DIR}/${UPDATE_UNIT}" "${UNIT_DIR}/${UPDATE_TIMER}" "${UNIT_DIR}/${REQUEST_UNIT}" "${UNIT_DIR}/${REQUEST_TIMER}"
    svc daemon-reload
    rm -f "$BIN_FILE"
    rm -f "$LT_SIDECAR_FILE"
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
    lt_sidecar_line
    echo "Ports:   API $(resolve_api_port)   Web $(resolve_web_port)"
    echo "Autostart: $(autostart_state)"
    echo "AutoUpdate: $(au_status_line)"
    local au_last
    au_last="$(au_value LAST_CHECK)"
    if [ -n "$au_last" ]; then
        echo "LastCheck: $au_last ($(au_value LAST_RESULT))"
    fi
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

# autostart_state — "enabled" when both services start at boot.
autostart_state() {
    if ! have_systemd; then echo "unknown (no systemd)"; return; fi
    if systemctl is-enabled --quiet "$API_UNIT" 2>/dev/null \
        && systemctl is-enabled --quiet "$WEB_UNIT" 2>/dev/null; then
        echo "enabled"
    else
        echo "disabled"
    fi
}

do_start() {
    require_root
    svc start "$API_UNIT" "$WEB_UNIT" || { warn "failed to start the services (installed? check: systemctl status $API_UNIT)"; return 1; }
    wait_health || true
}

do_stop() {
    require_root
    svc stop "$WEB_UNIT" "$API_UNIT" || { warn "failed to stop the services"; return 1; }
    log "Services stopped (API + web). Start them again with: dhtsearch start"
}

do_restart_cmd() {
    require_root
    validate_ports
    check_ports_free
    restart_services
    wait_health || true
}

# ------------------------------------------------------------- config admin

# mask_config_text — print the config file with secrets hidden: the
# password inside any postgres URL, and the value of every key whose
# name contains TOKEN / KEY / SECRET / PASSWORD.
mask_config_text() {
    sed -E \
        -e 's#(postgres(ql)?://[^:/@]+:)[^@]*@#\1***@#g' \
        -e 's#^([A-Za-z0-9_]*(TOKEN|KEY|SECRET|PASSWORD)[A-Za-z0-9_]*=).+$#\1*** (set)#' \
        "$ENV_FILE"
}

do_config_show() {
    [ -f "$ENV_FILE" ] || die "config not found: $ENV_FILE (not installed?)"
    mask_config_text
}

# do_set_token [VALUE] — set/reset ADMIN_TOKEN in the config file.
# Without VALUE a random token is generated and printed exactly once.
do_set_token() {
    require_root
    [ -f "$ENV_FILE" ] || die "config not found: $ENV_FILE (not installed?)"
    local tok="${1:-}"
    if [ -z "$tok" ]; then
        if command -v openssl >/dev/null 2>&1; then
            tok="$(openssl rand -hex 32)"
        else
            tok="$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')"
        fi
        log "New ADMIN_TOKEN (shown only this once — save it):"
        log "  $tok"
    fi
    set_env_value ADMIN_TOKEN "$tok"
    fix_permissions
    log "ADMIN_TOKEN written to $ENV_FILE. It takes effect after an API restart:"
    log "  systemctl restart $API_UNIT   (or run: dhtsearch restart)"
}

# ------------------------------------------------------------ backup/restore

# epoch_now — seconds with a fractional part (bash >= 5 EPOCHREALTIME),
# falling back to whole seconds on older shells. Used only to time
# backup operations for the log line, never for file names.
epoch_now() {
    printf '%s' "${EPOCHREALTIME:-${SECONDS}.0}"
}

# elapsed_since START — seconds between START (an epoch_now value) and
# now, with one decimal place.
elapsed_since() {
    local start="$1" now whole frac
    now="$(epoch_now)"
    whole=$(( ${now%.*} - ${start%.*} ))
    frac=$(( (10#${now#*.} - 10#${start#*.}) / 100000 ))
    if [ "$frac" -lt 0 ]; then
        whole=$(( whole - 1 ))
        frac=$(( frac + 10 ))
    fi
    printf '%d.%d' "$whole" "$frac"
}

# snapshot_tree SRC DEST — snapshot the directory tree SRC as DEST and
# print the path created (DEST, or DEST.tar in the tar fallback).
# A plain cp -a of the web tree walks thousands of small files and was
# the slow part of every upgrade. Release web trees are immutable once
# installed and installs always replace the tree wholesale
# (rm -rf + copy in install_package_files), so a hardlink copy
# (cp -al) is safe: near-instant, and the snapshot costs no extra disk
# while the files are shared. When hardlinks are unavailable (SRC and
# DEST on different filesystems) fall back to a single tar archive —
# one sequential write still beats thousands of small-file copies —
# and finally to the plain copy.
snapshot_tree() {
    local src="$1" dest="$2"
    rm -rf "$dest" "$dest.tar"
    if cp -al "$src" "$dest" 2>/dev/null; then
        printf '%s\n' "$dest"
        return 0
    fi
    rm -rf "$dest"
    warn "hardlink snapshot of $src unavailable (different filesystem?); falling back to a tar archive"
    if tar -cf "$dest.tar" -C "$(dirname "$src")" "$(basename "$src")" 2>/dev/null; then
        printf '%s\n' "$dest.tar"
        return 0
    fi
    rm -f "$dest.tar"
    warn "tar snapshot failed too; falling back to a full copy of $src"
    "${CP_A[@]}" "$src" "$dest" || die "failed to snapshot $src -> $dest"
    printf '%s\n' "$dest"
}

# restore_tree SNAPSHOT DEST — put back a tree captured by
# snapshot_tree, in either its directory or its .tar form. Restoring a
# hardlink snapshot hardlinks it back, so a rollback is as fast as the
# snapshot was instead of another full small-file copy.
restore_tree() {
    local snap="$1" dest="$2"
    rm -rf "$dest"
    case "$snap" in
        *.tar)
            "${TAR_EXTRACT_PLAIN[@]}" "$snap" -C "$(dirname "$dest")"
            ;;
        *)
            cp -al "$snap" "$dest" 2>/dev/null || "${CP_A[@]}" "$snap" "$dest"
            ;;
    esac
}

# rotate_backups [protect_ts] — prune old backups in $BACKUP_DIR:
# upgrade snapshot sets (dhtsearch-server.<ts>.bak together with the
# matching dhtsearch.env.<ts>.bak and web.<ts>.bak[.tar]) keep the
# newest KEEP_UPGRADE_SETS sets; database backups (db-*, including
# db-pre-restore-*) keep the newest KEEP_DB_BACKUPS files. The set
# stamped protect_ts — the one the running upgrade just took or just
# rolled back to — is never deleted. Every deletion is logged so the
# operator can see the backups dir being kept in check. Rotation is
# housekeeping: it must never fail the upgrade or backup that called
# it, so it always returns 0.
rotate_backups() {
    local protect="${1:-}"
    [ -d "$BACKUP_DIR" ] || return 0
    local f b t victim i drop
    # Upgrade snapshot sets, newest timestamp first (timestamps are
    # fixed-width, so lexical order is chronological).
    local sets=() sorted=()
    for f in "$BACKUP_DIR"/dhtsearch-server.*.bak; do
        [ -f "$f" ] || continue
        b="$(basename "$f")"
        t="${b#dhtsearch-server.}"
        sets+=("${t%.bak}")
    done
    if [ "${#sets[@]}" -gt "$KEEP_UPGRADE_SETS" ]; then
        while IFS= read -r t; do
            sorted+=("$t")
        done <<EOF
$(printf '%s\n' "${sets[@]}" | sort -r)
EOF
        for (( i = KEEP_UPGRADE_SETS; i < ${#sorted[@]}; i++ )); do
            victim="${sorted[i]}"
            [ "$victim" = "$protect" ] && continue
            rm -rf "${BACKUP_DIR}/web.${victim}.bak" "${BACKUP_DIR}/web.${victim}.bak.tar"
            rm -f "${BACKUP_DIR}/dhtsearch-server.${victim}.bak" "${BACKUP_DIR}/dhtsearch.env.${victim}.bak"
            log "Rotated out upgrade backup set $victim (keeping the newest $KEEP_UPGRADE_SETS sets)"
        done
    fi
    # Database backups, oldest first (list_db_backups order); the file
    # a backup just wrote carries the newest timestamp and sorts last,
    # so pruning from the front never touches it.
    local dbs=()
    while IFS= read -r f; do
        [ -n "$f" ] && dbs+=("$f")
    done <<EOF
$(list_db_backups)
EOF
    if [ "${#dbs[@]}" -gt "$KEEP_DB_BACKUPS" ]; then
        drop=$(( ${#dbs[@]} - KEEP_DB_BACKUPS ))
        for (( i = 0; i < drop; i++ )); do
            rm -f "${dbs[i]}" "${dbs[i]}-wal" "${dbs[i]}-shm"
            log "Rotated out database backup ${dbs[i]} (keeping the newest $KEEP_DB_BACKUPS)"
        done
    fi
    return 0
}

# backup_db_to OUT — write a database backup to OUT, using whichever
# backend the config selects. SQLite: sqlite3 .backup when the CLI is
# available (safe against a running server), else a raw file copy of
# the db plus its WAL sidecars, with a warning. PostgreSQL: pg_dump
# with --clean --if-exists so a restore is a plain psql -f.
backup_db_to() {
    local out="$1"
    if [ "$(db_backend)" = "postgres" ]; then
        command -v pg_dump >/dev/null 2>&1 || { warn "pg_dump not found (install the postgresql-client package)"; return 1; }
        pg_dump --clean --if-exists "$(env_value DATABASE_URL)" > "$out"
        return $?
    fi
    local db real_db
    db="$(sqlite_db_path)"
    real_db="${PREFIX}${db}"
    [ -f "$real_db" ] || { warn "SQLite database not found: $db"; return 1; }
    if command -v sqlite3 >/dev/null 2>&1; then
        sqlite3 "$real_db" ".backup '$out'"
        return $?
    fi
    cp -a "$real_db" "$out" || return 1
    [ -f "${real_db}-wal" ] && cp -a "${real_db}-wal" "${out}-wal"
    [ -f "${real_db}-shm" ] && cp -a "${real_db}-shm" "${out}-shm"
    warn "sqlite3 CLI not found: copied the raw database file (plus WAL sidecars). For a perfectly consistent snapshot, stop the service first (dhtsearch stop) and re-run the backup."
    return 0
}

do_backup() {
    require_root
    [ -f "$ENV_FILE" ] || die "config not found: $ENV_FILE (not installed?)"
    mkdir -p "$BACKUP_DIR"
    local ts out bk_start
    ts="$(date +%Y%m%d%H%M%S)"
    if [ "$(db_backend)" = "postgres" ]; then
        out="${BACKUP_DIR}/db-${ts}.sql"
    else
        out="${BACKUP_DIR}/db-${ts}.sqlite"
    fi
    log "Backing up database ($(db_display)) -> $out"
    bk_start="$(epoch_now)"
    backup_db_to "$out" || die "backup failed (see above); nothing was restored or changed"
    log "Backup done: $out ($(du -h "$out" | cut -f1), $(elapsed_since "$bk_start")s)"
    rotate_backups
}

# list_db_backups — database backup files, oldest first.
list_db_backups() {
    find "$BACKUP_DIR" -maxdepth 1 -type f -name 'db-*' 2>/dev/null | sort
}

do_backups() {
    local f found=0
    while IFS= read -r f; do
        [ -n "$f" ] || continue
        found=1
        printf '%s  %s\n' "$(du -h "$f" | cut -f1)" "$f"
    done <<EOF
$(list_db_backups)
EOF
    if [ "$found" -eq 0 ]; then
        echo "暂无数据库备份（$BACKUP_DIR 下没有 db-* 文件）。用 backup 先备一份。"
    fi
}

# do_restore FILE — restore a backup made by do_backup. Stops the
# services first and snapshots the current database to a
# db-pre-restore-* file before overwriting anything, so a bad restore
# can itself be undone with the same command.
do_restore() {
    require_root
    local file="${1:-}"
    [ -n "$file" ] || die "usage: restore <backup-file> (see: backups)"
    if [ ! -f "$file" ] && [ -f "${BACKUP_DIR}/${file}" ]; then
        file="${BACKUP_DIR}/${file}"
    fi
    [ -f "$file" ] || die "backup file not found: $1"
    [ -f "$ENV_FILE" ] || die "config not found: $ENV_FILE (not installed?)"
    if [ "$(db_backend)" = "postgres" ]; then
        case "$file" in
            *.sql) ;;
            *) die "$file is not a PostgreSQL dump (.sql), but the current backend is PostgreSQL" ;;
        esac
        command -v psql >/dev/null 2>&1 || die "psql not found (install the postgresql-client package) — cannot restore PostgreSQL"
    else
        case "$file" in
            *.sql) die "$file is a PostgreSQL dump, but the current backend is SQLite; switch the database first (menu: 数据库切换)" ;;
        esac
    fi

    log "Stopping services for the restore"
    svc stop "$WEB_UNIT" "$API_UNIT"
    local ts pre
    ts="$(date +%Y%m%d%H%M%S)"
    if [ "$(db_backend)" = "postgres" ]; then
        pre="${BACKUP_DIR}/db-pre-restore-${ts}.sql"
    else
        pre="${BACKUP_DIR}/db-pre-restore-${ts}.sqlite"
    fi
    if backup_db_to "$pre"; then
        log "Current database snapshotted first: $pre"
    else
        warn "could not snapshot the current database; continuing without a pre-restore backup"
        pre=""
    fi

    if [ "$(db_backend)" = "postgres" ]; then
        if ! psql "$(env_value DATABASE_URL)" -v ON_ERROR_STOP=1 -q -f "$file"; then
            warn "PostgreSQL restore failed partway. Pre-restore snapshot: ${pre:-none}"
            restart_services
            wait_health || true
            die "restore failed; services were restarted — check the state before trying again"
        fi
    else
        local db real_db
        db="$(sqlite_db_path)"
        real_db="${PREFIX}${db}"
        mkdir -p "$(dirname "$real_db")"
        cp -a "$file" "$real_db" || die "failed to write the restored database to $db"
        # A WAL from the pre-restore database must not be replayed
        # over the restored file; the backup's own sidecars (taken by
        # the raw-copy fallback) belong to the backup and do apply.
        if [ -f "${file}-wal" ]; then
            cp -a "${file}-wal" "${real_db}-wal"
            [ -f "${file}-shm" ] && cp -a "${file}-shm" "${real_db}-shm"
        else
            rm -f "${real_db}-wal" "${real_db}-shm"
        fi
        fix_permissions
    fi
    restart_services
    if wait_health; then
        log "Restored database from $file"
    else
        warn "restore applied but the health check failed; pre-restore snapshot: ${pre:-none}; logs: journalctl -u $API_UNIT -n 100"
        return 1
    fi
}

# ---------------------------------------------------------------- selfcheck

# pg_reachable — TCP-level reachability of the configured PostgreSQL.
pg_reachable() {
    local url rest hostport host port
    url="$(env_value DATABASE_URL)"
    rest="${url#*://}"
    rest="${rest#*@}"
    hostport="${rest%%/*}"
    host="${hostport%%:*}"
    if [ "$hostport" != "$host" ]; then port="${hostport##*:}"; else port="5432"; fi
    [ -n "$host" ] || return 1
    if command -v pg_isready >/dev/null 2>&1; then
        pg_isready -h "$host" -p "$port" -t 3 >/dev/null 2>&1
    else
        timeout 3 bash -c "exec 3<>/dev/tcp/${host}/${port}" 2>/dev/null
    fi
}

# do_selfcheck — one pass over everything an operator would check by
# hand. Returns non-zero when any hard check fails.
do_selfcheck() {
    local fail=0 ap wp
    ap="$(resolve_api_port)"
    wp="$(resolve_web_port)"
    echo "DHTSearch 自检（$(date '+%Y-%m-%d %H:%M:%S')）"
    echo "版本：    $(installed_version)"
    if have_systemd; then
        local a w
        a="$(systemctl is-active "$API_UNIT" 2>/dev/null || echo unknown)"
        w="$(systemctl is-active "$WEB_UNIT" 2>/dev/null || echo unknown)"
        echo "服务：    API $a / Web $w"
        if [ "$a" != "active" ] || [ "$w" != "active" ]; then fail=1; fi
    else
        echo "服务：    无 systemd，跳过状态检查"
    fi
    if [ -n "$(port_listeners "$ap")" ]; then
        echo "端口：    API $ap 监听中"
    else
        echo "端口：    API $ap 无监听"; fail=1
    fi
    if [ -n "$(port_listeners "$wp")" ]; then
        echo "端口：    Web $wp 监听中"
    else
        echo "端口：    Web $wp 无监听"; fail=1
    fi
    if curl -fsS "http://127.0.0.1:${ap}/api/healthz" >/dev/null 2>&1; then
        echo "健康：    API healthz OK"
    else
        echo "健康：    API healthz 无响应"; fail=1
    fi
    if curl -fsS "http://127.0.0.1:${wp}/" >/dev/null 2>&1; then
        echo "健康：    Web OK"
    else
        echo "健康：    Web 无响应"; fail=1
    fi
    if [ -d "$DATA_DIR" ]; then
        echo "磁盘：    数据目录所在盘 $(df -h "$DATA_DIR" | awk 'NR==2 {print $4 " 可用 / 共 " $2 "（已用 " $5 "）"}')"
    else
        echo "磁盘：    数据目录不存在（$DATA_DIR）"; fail=1
    fi
    if [ "$(db_backend)" = "postgres" ]; then
        if pg_reachable; then
            echo "数据库：  PostgreSQL 可连接（$(mask_pg_url "$(env_value DATABASE_URL)")）"
        else
            echo "数据库：  PostgreSQL 连接失败（$(mask_pg_url "$(env_value DATABASE_URL)")）"; fail=1
        fi
    else
        local db real_db
        db="$(sqlite_db_path)"
        real_db="${PREFIX}${db}"
        if [ -f "$real_db" ]; then
            echo "数据库：  SQLite 文件正常（$db，$(du -h "$real_db" | cut -f1)）"
        else
            echo "数据库：  SQLite 文件不存在（$db）"; fail=1
        fi
    fi
    if have_systemd; then
        local errs=""
        errs="$(journalctl -u "$API_UNIT" -u "$WEB_UNIT" -p err --since "-24 hours" --no-pager -q 2>/dev/null | wc -l)" || errs="?"
        echo "日志：    近 24 小时 error 级别日志 $errs 条"
        if [ "$errs" != "?" ] && [ "$errs" -gt 0 ]; then
            journalctl -u "$API_UNIT" -u "$WEB_UNIT" -p err --since "-24 hours" --no-pager -q 2>/dev/null | tail -3 | sed 's/^/          /' || true
        fi
    fi
    echo "开机自启：$(autostart_state)"
    echo "自动更新：$(au_status_line)"
    if [ "$fail" -eq 0 ]; then
        echo "结论：    全部正常"
    else
        echo "结论：    有异常项（见上）"
    fi
    return "$fail"
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

# menu_script_sha — sha256 of the installed management entry
# ($MANAGER_FILE), "" when it does not exist (yet).
menu_script_sha() {
    if [ -f "$MANAGER_FILE" ]; then
        sha256sum "$MANAGER_FILE" 2>/dev/null | awk '{print $1}'
    fi
}

# menu_is_script FILE — FILE is an executable script (the check
# Polaris's install-panel.sh does before exec'ing a replaced entry).
menu_is_script() {
    [ -f "$1" ] && [ -x "$1" ] && head -c 2 "$1" 2>/dev/null | grep -q '#!'
}

# menu_reexec_if_updated BEFORE_SHA — an upgrade replaces the
# management entry (install_manager) while this process keeps
# running the old code from memory; when the entry actually
# changed, exec the new copy so the menu continues on current code
# instead of stale code. No-op when the entry is unchanged or is
# not a script.
menu_reexec_if_updated() {
    local after
    after="$(menu_script_sha)"
    if [ -n "$after" ] && [ "$after" != "$1" ] && menu_is_script "$MANAGER_FILE"; then
        echo "管理脚本已随升级更新，正在用新版脚本重新打开菜单..."
        exec "$MANAGER_FILE"
    fi
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
    local before
    before="$(menu_script_sha)"
    echo "当前版本：$(installed_version)"
    if ! confirm "确认升级？（先备份当前版本，健康检查失败自动回滚）"; then menu_pause; return 0; fi
    do_upgrade
    menu_reexec_if_updated "$before"
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

menu_start() {
    if ! confirm "确认启动 API 和 Web 两个服务？"; then menu_pause; return 0; fi
    do_start || warn "启动未成功，请看日志：journalctl -u $API_UNIT -n 50"
    menu_pause
}

menu_stop() {
    echo "停止后网站将无法访问，直到再次启动。"
    if ! confirm "确认停止 API 和 Web 两个服务？"; then menu_pause; return 0; fi
    do_stop || warn "停止未成功，请看日志：journalctl -u $API_UNIT -n 50"
    menu_pause
}

menu_autostart() {
    echo "开机自启：当前为 $(autostart_state)（$API_UNIT / $WEB_UNIT）"
    echo "  1) 开启开机自启"
    echo "  2) 关闭开机自启"
    echo "  0) 返回"
    printf '选择： '
    local c
    read -r c || return 0
    case "$c" in
        1)
            svc enable "$API_UNIT" "$WEB_UNIT" || warn "开启失败"
            echo "开机自启：$(autostart_state)"
            ;;
        2)
            svc disable "$API_UNIT" "$WEB_UNIT" || warn "关闭失败"
            echo "开机自启：$(autostart_state)"
            ;;
        *) return 0 ;;
    esac
    menu_pause
}

menu_config() {
    require_root
    if [ ! -f "$ENV_FILE" ]; then
        echo "尚未安装（找不到 $ENV_FILE），请先选「安装」。"
        menu_pause
        return 0
    fi
    local c t v ed
    while true; do
        echo
        echo "配置管理（$ENV_FILE）"
        echo "  1) 查看当前配置（密钥脱敏）"
        echo "  2) 设置 / 重置 ADMIN_TOKEN"
        echo "  3) 编辑配置文件（改完需重启服务生效）"
        echo "  0) 返回"
        printf '选择： '
        if ! read -r c; then return 0; fi
        case "$c" in
            1)
                mask_config_text
                menu_pause
                ;;
            2)
                echo "  1) 随机生成一个新令牌"
                echo "  2) 手动输入令牌"
                printf '选择（默认 1）： '
                read -r t || t="1"
                case "$t" in
                    2)
                        printf '输入 ADMIN_TOKEN（空则取消）: '
                        read -r v || v=""
                        if [ -n "$v" ]; then
                            do_set_token "$v"
                        else
                            echo "已取消。"
                        fi
                        ;;
                    *) do_set_token ;;
                esac
                if confirm "现在重启两个服务让新令牌生效？"; then
                    restart_services
                    wait_health || true
                fi
                menu_pause
                ;;
            3)
                ed="${EDITOR:-vi}"
                if command -v "$ed" >/dev/null 2>&1; then
                    "$ed" "$ENV_FILE" || true
                    chmod 600 "$ENV_FILE" || true
                    echo "配置已保存。多数改动需要重启服务才生效。"
                    if confirm "现在重启两个服务？"; then
                        restart_services
                        wait_health || true
                    fi
                else
                    echo "找不到编辑器 $ed，请手动编辑：$ENV_FILE"
                fi
                menu_pause
                ;;
            0) return 0 ;;
            *) echo "未知选择：$c" ;;
        esac
    done
}

menu_restore() {
    local files=() f i c target
    while IFS= read -r f; do
        [ -n "$f" ] && files+=("$f")
    done <<EOF
$(list_db_backups)
EOF
    if [ "${#files[@]}" -eq 0 ]; then
        echo "暂无可恢复的备份。"
        menu_pause
        return 0
    fi
    echo "选择要恢复的备份（当前数据库：$(db_display)）："
    for i in "${!files[@]}"; do
        printf '  %d) %s（%s）\n' "$((i + 1))" "${files[$i]}" "$(du -h "${files[$i]}" | cut -f1)"
    done
    echo "  0) 取消"
    printf '选择： '
    read -r c || return 0
    case "$c" in
        ''|*[!0-9]*) echo "已取消。"; menu_pause; return 0 ;;
    esac
    if [ "$c" -lt 1 ] || [ "$c" -gt "${#files[@]}" ]; then
        echo "已取消。"
        menu_pause
        return 0
    fi
    target="${files[$((c - 1))]}"
    echo "将从 $target 恢复。恢复前会停服务，并先把当前数据库另存一份 db-pre-restore 备份。"
    if ! confirm "确认恢复？现有数据将被备份文件覆盖"; then menu_pause; return 0; fi
    # Subshell: do_restore may die on failure; the menu must survive.
    ( do_restore "$target" ) || echo "恢复未完成（见上面的输出）。"
    menu_pause
}

menu_backup() {
    require_root
    if [ ! -f "$ENV_FILE" ]; then
        echo "尚未安装（找不到 $ENV_FILE），请先选「安装」。"
        menu_pause
        return 0
    fi
    local c
    while true; do
        echo
        echo "备份与恢复（当前数据库：$(db_display)）"
        echo "备份目录：$BACKUP_DIR"
        echo "  1) 立即备份"
        echo "  2) 备份列表"
        echo "  3) 从备份恢复"
        echo "  0) 返回"
        printf '选择： '
        if ! read -r c; then return 0; fi
        case "$c" in
            1)
                # Subshell: do_backup may die on failure; the menu
                # must survive.
                ( do_backup ) || echo "备份未完成（见上面的输出）。"
                menu_pause
                ;;
            2) do_backups; menu_pause ;;
            3) menu_restore ;;
            0) return 0 ;;
            *) echo "未知选择：$c" ;;
        esac
    done
}

menu_selfcheck() {
    do_selfcheck || true
    menu_pause
}

menu_check_update() {
    local rc=0 before=""
    do_check_update || rc=$?
    if [ "$rc" -eq 2 ]; then
        if confirm "现在升级到最新版本？"; then
            before="$(menu_script_sha)"
            do_upgrade
            menu_reexec_if_updated "$before"
        fi
    fi
    menu_pause
}

menu_auto_update() {
    require_root
    local lc c
    echo "自动更新：当前 $(au_status_line)"
    lc="$(au_value LAST_CHECK)"
    if [ -n "$lc" ]; then
        echo "上次检查：$lc（$(au_value LAST_RESULT)）"
    fi
    echo "开启后，systemd 定时器会在凌晨 03:37 前后（带最多 30 分钟随机延迟）"
    echo "自动执行 upgrade：先备份、健康检查失败自动回滚，和手动升级同一套流程。"
    echo "  1) 开启（每天）"
    echo "  2) 开启（每周一）"
    echo "  3) 关闭"
    echo "  0) 返回"
    printf '选择： '
    read -r c || return 0
    case "$c" in
        1) au_set on daily ;;
        2) au_set on weekly ;;
        3) au_set off ;;
        *) return 0 ;;
    esac
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
    # Destructive actions confirm with a default-no prompt; the
    # purge path is irreversible, so it confirms twice — once for
    # the uninstall itself, once for deleting the data.
    if [ "$PURGE" -eq 1 ]; then
        if ! confirm "确认卸载 DHTSearch？"; then menu_pause; return 0; fi
        if ! confirm "确认同时删除全部配置和数据？此操作不可恢复"; then menu_pause; return 0; fi
    else
        if ! confirm "确认卸载 DHTSearch？（配置和数据会保留）"; then menu_pause; return 0; fi
    fi
    do_uninstall
    echo "卸载完成。"
    exit 0
}

# menu_installed — the menu's notion of "there is an install to
# manage": any durable trace of one (version record, config,
# service binary, unit files). Items that operate on an install
# are guarded by it, so a fresh machine gets a friendly 未安装
# notice instead of a handler failing halfway (or, for upgrade, a
# die() that kills the whole menu).
menu_installed() {
    [ -f "$VERSION_FILE" ] || [ -f "$ENV_FILE" ] || [ -f "$BIN_FILE" ] \
        || [ -f "${UNIT_DIR}/${API_UNIT}" ] || [ -f "${UNIT_DIR}/${WEB_UNIT}" ]
}

# menu_require_installed — the guard behind every service / config
# / data item (and uninstall): true when installed; otherwise
# print the notice, pause, and return false so the caller skips
# the handler. 安装 itself stays unguarded.
menu_require_installed() {
    if menu_installed; then return 0; fi
    echo "尚未安装，请先选「15) 安装 / 重新安装」。"
    menu_pause
    return 1
}

# menu_kv LABEL VALUE — one header status line ("label：value"),
# the Polaris kv shape.
menu_kv() { printf '  %s：%s\n' "$1" "$2"; }

# menu_row N LABEL — one menu item per line (items never share a
# line: columns fall apart when a narrow terminal wraps them).
menu_row() { printf '  %s) %s\n' "$1" "$2"; }

# menu_section TITLE — a group header between item blocks.
menu_section() { printf '\n%s\n' "$1"; }

# menu_svc_state UNIT — the header's state for one service. With
# systemd it is the live state; without it (dry-run, the
# DHTSEARCH_ROOT test hooks) only what the unit file proves —
# 已安装（状态未知）— and "" when there is no unit file either, so
# the caller omits the line instead of guessing a state.
menu_svc_state() {
    if have_systemd; then
        if systemctl is-active --quiet "$1" 2>/dev/null; then
            echo "● 运行中"
        else
            echo "○ 已停止"
        fi
    elif [ -f "${UNIT_DIR}/$1" ]; then
        echo "已安装（状态未知）"
    fi
}

# menu_au_label — the 自动更新 header value: 关, or 开 with its
# schedule (au_time already resolves panel vs built-in time).
menu_au_label() {
    if ! au_enabled; then echo "关"; return; fi
    local when="每天"
    [ "$(au_freq)" = "weekly" ] && when="每周一"
    echo "开（${when} $(au_time)）"
}

# menu_sys_line — "Debian 13 · x86_64 · 负载 0.42", built from
# local files only, cheap enough to render on every loop.
menu_sys_line() {
    local os load
    os="$(awk -F'"' '/^NAME=/ { split($2, a, " "); n = a[1] } /^VERSION_ID=/ { v = $2 } END { if (n == "") print ""; else print n, v }' /etc/os-release 2>/dev/null)"
    load="$(awk '{print $1}' /proc/loadavg 2>/dev/null)"
    printf '%s · %s · 负载 %s' "${os:-Linux}" "$(uname -m)" "${load:-?}"
}

# menu_header — screen clear, title, then kv status lines, redrawn
# on every loop (the Polaris model): the glance questions (what
# version? is each service up? does it update itself?) are answered
# before anything is picked; 状态 keeps the full detail. Pieces
# that cannot be determined are omitted rather than guessed.
# Once the background latest-version lookup (see menu) has landed,
# a 发现新版本 hint points at the upgrade item.
menu_header() {
    clear 2>/dev/null || true
    echo "DHTSearch 管理菜单"
    if menu_installed; then
        local ver svc latest=""
        ver="$(installed_version)"
        if [ "$ver" != "unknown" ]; then
            menu_kv "版本" "$ver"
        else
            menu_kv "版本" "已安装（版本未知）"
        fi
        svc="$(menu_svc_state "$API_UNIT")"
        [ -n "$svc" ] && menu_kv "API 服务" "$svc"
        svc="$(menu_svc_state "$WEB_UNIT")"
        [ -n "$svc" ] && menu_kv "Web 服务" "$svc"
        menu_kv "自动更新" "$(menu_au_label)"
        menu_kv "系统" "$(menu_sys_line)"
        if [ -n "$MENU_LATEST_FILE" ] && [ -s "$MENU_LATEST_FILE" ]; then
            latest="$(cat "$MENU_LATEST_FILE")"
        fi
        if [ -n "$latest" ] && [ "$latest" != "$ver" ]; then
            menu_kv "更新" "发现新版本 $latest，选 7 升级到最新版"
        fi
    else
        menu_kv "状态" "未安装（选 15 安装）"
        menu_kv "系统" "$(menu_sys_line)"
    fi
}

# The menu is grouped by task domain — day-to-day service operations
# first, then updates, configuration and data, with install and the
# destructive uninstall last under 系统. Numbering stays single-level
# and stable within a release so muscle memory and docs keep working;
# every entry calls the same handler the flat menu called. The
# interaction model follows Polaris's install-panel.sh menu: a kv
# status header redrawn on every loop, live state inside item
# labels, a ranged prompt, not-installed guards, and a self-exec
# after an upgrade replaces this script (see menu_reexec_if_updated).
menu() {
    local choice au_mark as_mark
    # Look up the newest release in the background so the menu opens
    # at once; menu_header shows the 发现新版本 hint once the lookup
    # lands (usually from the second render on). Dry-run and the
    # DHTSEARCH_ROOT test hooks never start it: no network there.
    MENU_LATEST_FILE="$(mktemp)"
    if menu_installed && [ "$DRY_RUN" != "1" ] && [ "${DHTSEARCH_SKIP_SYSTEMD:-0}" != "1" ]; then
        ( resolve_latest_version_quiet > "$MENU_LATEST_FILE" ) &
    fi
    while true; do
        menu_header
        au_mark="关"
        au_enabled && au_mark="开"
        as_mark=""
        case "$(autostart_state)" in
            enabled) as_mark=" [开]" ;;
            disabled) as_mark=" [关]" ;;
        esac
        menu_section "服务"
        menu_row 1 "状态"
        menu_row 2 "查看日志"
        menu_row 3 "重启服务"
        menu_row 4 "启动服务"
        menu_row 5 "停止服务"
        menu_section "更新"
        menu_row 6 "检查更新"
        menu_row 7 "升级到最新版"
        menu_row 8 "自动更新设置 [$au_mark]"
        menu_section "配置"
        menu_row 9 "修改端口"
        menu_row 10 "数据库切换"
        menu_row 11 "配置管理"
        menu_row 12 "开机自启$as_mark"
        menu_section "数据"
        menu_row 13 "备份与恢复"
        menu_row 14 "自检"
        menu_section "系统"
        menu_row 15 "安装 / 重新安装"
        menu_row 16 "卸载"
        menu_row 0 "退出"
        printf '请选择 [0-16]： '
        if ! read -r choice; then exit 0; fi
        case "$choice" in
            1) menu_require_installed && { do_status; menu_pause; } ;;
            2) menu_require_installed && menu_logs ;;
            3) menu_require_installed && menu_restart ;;
            4) menu_require_installed && menu_start ;;
            5) menu_require_installed && menu_stop ;;
            6) menu_require_installed && menu_check_update ;;
            7) menu_require_installed && menu_upgrade ;;
            8) menu_require_installed && menu_auto_update ;;
            9) menu_require_installed && menu_change_port ;;
            10) menu_require_installed && menu_switch_db ;;
            11) menu_require_installed && menu_config ;;
            12) menu_require_installed && menu_autostart ;;
            13) menu_require_installed && menu_backup ;;
            14) menu_require_installed && menu_selfcheck ;;
            15) menu_install ;;
            16) menu_require_installed && menu_uninstall ;;
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
            --yes) YES_OPT=1; shift ;;
            --check) CHECK_OPT=1; shift ;;
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
        start) do_start ;;
        stop) do_stop ;;
        restart) do_restart_cmd ;;
        check-update)
            local rc=0
            do_check_update || rc=$?
            exit "$rc"
            ;;
        backup) do_backup ;;
        backups) do_backups ;;
        restore)
            shift
            local rfile=""
            while [ $# -gt 0 ]; do
                case "$1" in
                    --yes) YES_OPT=1 ;;
                    *) rfile="$1" ;;
                esac
                shift
            done
            # Interactive runs confirm first; --yes (and any
            # non-terminal caller) skips the prompt.
            if [ -t 0 ] && [ "$YES_OPT" != "1" ]; then
                confirm "确认从备份 ${rfile} 恢复数据库？（先停服务，并给当前库留一份 pre-restore 备份）" || exit 1
            fi
            do_restore "$rfile"
            ;;
        selfcheck)
            local sc_rc=0
            do_selfcheck || sc_rc=$?
            exit "$sc_rc"
            ;;
        config) do_config_show ;;
        set-token) shift; do_set_token "${1:-}" ;;
        autostart)
            shift
            case "${1:-status}" in
                on) require_root; svc enable "$API_UNIT" "$WEB_UNIT" ;;
                off) require_root; svc disable "$API_UNIT" "$WEB_UNIT" ;;
                status) echo "开机自启：$(autostart_state)" ;;
                *) die "usage: install.sh autostart on|off|status" ;;
            esac
            ;;
        upgrade-request) require_root; do_upgrade_request ;;
        auto-update)
            shift
            case "${1:-status}" in
                on) require_root; au_set on "${2:-}" ;;
                off) require_root; au_set off ;;
                status)
                    echo "自动更新：$(au_status_line)"
                    local lc
                    lc="$(au_value LAST_CHECK)"
                    if [ -n "$lc" ]; then
                        echo "上次检查：$lc（$(au_value LAST_RESULT)）"
                    fi
                    ;;
                *) die "usage: install.sh auto-update on [daily|weekly]|off|status" ;;
            esac
            ;;
        help|-h|--help) usage ;;
        "")
            if [ -t 0 ]; then menu; else usage; exit 2; fi
            ;;
        *) usage >&2; die "unknown command: $cmd" ;;
    esac
}

main "$@"
