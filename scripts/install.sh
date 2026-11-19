#!/usr/bin/env bash
# install.sh — one-click install / upgrade / uninstall for DHTSearch.
#
#   bash <(curl -fsSL https://raw.githubusercontent.com/cuijianzhuang/dhtsearch/main/scripts/install.sh) install
#
# Subcommands: install | upgrade | uninstall | status
# Run with no arguments in a terminal for an interactive menu.
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
L_WEB_DIR="/opt/dhtsearch/web"
L_CONF_DIR="/etc/dhtsearch"
L_ENV_FILE="/etc/dhtsearch/dhtsearch.env"
L_DATA_DIR="/var/lib/dhtsearch"

# Filesystem paths (prefixed under DHTSEARCH_ROOT when testing).
PREFIX="${DHTSEARCH_ROOT:-}"
BIN_FILE="${PREFIX}${L_BIN}"
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
  --dry-run            write config/unit files but do not touch systemd
                       or start services

Options (uninstall):
  --purge              also delete /etc/dhtsearch and /var/lib/dhtsearch

Environment:
  DHTSEARCH_ROOT         prefix all install paths (testing)
  DHTSEARCH_SKIP_SYSTEMD=1
                         write units without calling systemctl (testing)
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

api_port() {
    local addr="127.0.0.1:8080"
    if [ -f "$ENV_FILE" ]; then
        local line
        line="$(grep -E '^HTTP_ADDR=' "$ENV_FILE" | tail -1 || true)"
        [ -n "$line" ] && addr="${line#HTTP_ADDR=}"
    fi
    echo "${addr##*:}"
}

write_env_file() {
    if [ -f "$ENV_FILE" ]; then
        log "Config exists, keeping it: $ENV_FILE"
        return
    fi
    log "Writing default config: $ENV_FILE"
    cat > "$ENV_FILE" <<EOF
# DHTSearch configuration — full reference: env.example in the repo/release.
# Values here are read by systemd (EnvironmentFile) and by the server itself.

# API + crawler. Loopback by default; put a reverse proxy in front for
# public access. The web service fetches this address server-side.
HTTP_ADDR=127.0.0.1:8080

# SQLite database (default). Lives in the data dir.
DB_PATH=${L_DATA_DIR}/dhtsearch.db

# Postgres instead of SQLite (optional): set a connection URL and DB_PATH
# is ignored. Tables/indexes are created on startup.
#DATABASE_URL=postgres://user:password@127.0.0.1:5432/dhtsearch?sslmode=disable

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
    local node_bin
    node_bin="$(command -v node)"
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
Environment=PORT=3000
Environment=HOSTNAME=127.0.0.1
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
    local port deadline
    port="$(api_port)"
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
        if curl -fsS "http://127.0.0.1:3000/" >/dev/null 2>&1; then
            log "Healthy: web OK on port 3000"
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
            chown "root:${SERVICE_USER}" "$ENV_FILE" || true
            chmod 640 "$ENV_FILE" || true
        fi
    fi
}

record_version() {
    printf '%s\n' "$1" > "$VERSION_FILE"
}

installed_version() {
    if [ -f "$VERSION_FILE" ]; then cat "$VERSION_FILE"; else echo "unknown"; fi
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
    local arch workdir tarball
    arch="$(detect_arch)"
    workdir="$(mktemp -d)"
    CLEANUP_DIR="$workdir"

    prepare_dirs_and_user
    obtain_package "$arch" "$workdir"
    tarball="$PKG_TARBALL"
    install_package_files "$tarball"
    write_env_file
    write_units
    fix_permissions
    restart_services
    if ! wait_health; then
        die "install finished but the service is not healthy; see logs: journalctl -u $API_UNIT -n 100"
    fi
    record_version "$RESOLVED_VERSION"
    log "Installed DHTSearch $RESOLVED_VERSION (arch $arch)"
    log "API: http://127.0.0.1:$(api_port)/api/healthz   Web: http://127.0.0.1:3000/"
    log "Config: $ENV_FILE   Data: $DATA_DIR   Logs: journalctl -u $API_UNIT -f"
}

do_upgrade() {
    require_root
    [ -f "$BIN_FILE" ] || die "not installed yet; run install first"
    check_platform
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
    rm -rf "$APP_DIR"
    log "Removed services, binary and web files."
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
    if have_systemd; then
        systemctl is-active "$API_UNIT" "$WEB_UNIT" || true
        systemctl status "$API_UNIT" "$WEB_UNIT" --no-pager -n 5 || true
    else
        echo "systemd: skipped (dry-run / no-systemd mode)"
    fi
    local port
    port="$(api_port)"
    if curl -fsS "http://127.0.0.1:${port}/api/healthz" >/dev/null 2>&1; then
        echo "Health:  OK (http://127.0.0.1:${port}/api/healthz)"
    else
        echo "Health:  not responding on port ${port}"
    fi
}

menu() {
    echo "DHTSearch installer"
    echo "  1) install"
    echo "  2) upgrade"
    echo "  3) status"
    echo "  4) uninstall"
    echo "  0) exit"
    printf 'Choose: '
    local choice
    read -r choice
    case "$choice" in
        1) do_install ;;
        2) do_upgrade ;;
        3) do_status ;;
        4) do_uninstall ;;
        0) exit 0 ;;
        *) die "unknown choice: $choice" ;;
    esac
}

# ---------------------------------------------------------------- main

parse_opts() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --version) VERSION="$2"; shift 2 ;;
            --from-source) FROM_SOURCE=1; shift ;;
            --file) LOCAL_FILE="$2"; shift 2 ;;
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
