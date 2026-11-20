#!/usr/bin/env bash
# test-release-tools.sh — local tests for the auto-release helper scripts
# (release-scope.sh and next-version.sh). No network, no GitHub Actions:
#
#   ./scripts/test-release-tools.sh
#
# Exits non-zero on the first failed assertion.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
SCOPE="scripts/release-scope.sh"
NEXT="scripts/next-version.sh"

fail() { echo "FAIL: $1" >&2; exit 1; }

scope_is() {
    # scope_is <expected> <file>...
    local expected="$1"; shift
    local got
    got="$(printf '%s\n' "$@" | "$SCOPE")"
    [ "$got" = "$expected" ] || fail "scope($*) = $got, want $expected"
}

scope_is false "README.md"
scope_is false "docs/design.md"
# scripts/install.sh is the shipped management entry point: it releases.
scope_is true  "scripts/install.sh"
# …but the rest of scripts/ is release tooling and still does not.
scope_is false "scripts/package-release.sh"
scope_is false "scripts/release-scope.sh"
scope_is false "scripts/next-version.sh"
scope_is false ".github/workflows/ci.yml"
scope_is false "env.example" "LICENSE"
scope_is false "web/AGENTS.md"
scope_is true  "server/internal/store/store.go"
scope_is true  "server/cmd/server/main.go"
scope_is true  "server/go.mod"
scope_is true  "server/go.sum"
scope_is true  "web/app/search/page.tsx"
scope_is true  "web/app/globals.css"
scope_is true  "web/components/ResultCard.tsx"
scope_is true  "web/lib/api.ts"
scope_is true  "web/public/next.svg"
scope_is true  "web/package.json"
scope_is true  "web/package-lock.json"
scope_is true  "web/next.config.ts"
scope_is true  "web/tsconfig.json"
scope_is true  "web/postcss.config.mjs"
# Business code mixed with docs/scripts still releases.
scope_is true  "README.md" "scripts/install.sh"
scope_is true  "README.md" "scripts/install.sh" "server/internal/api/api.go"
scope_is true  ".github/workflows/release.yml" "web/components/SearchBox.tsx"
# Empty change set never releases.
[ "$(printf '' | "$SCOPE")" = "false" ] || fail "scope(empty) != false"

next_is() {
    # next_is <expected> <tag>...
    local expected="$1"; shift
    local got
    if [ $# -eq 0 ]; then
        got="$(printf '' | "$NEXT")"
    else
        got="$(printf '%s\n' "$@" | "$NEXT")"
    fi
    [ "$got" = "$expected" ] || fail "next($*) = $got, want $expected"
}

next_is "v0.1.0"                      # no tags at all
next_is "v0.1.1" "v0.1.0"
next_is "v0.1.10" "v0.1.9" "v0.1.2"   # numeric, not lexical, ordering
next_is "v1.10.1" "v1.9.9" "v1.10.0"
next_is "v0.2.4" "v0.2.3" "v0.2.0-rc.1" "v0.3.0-beta" "nightly" "v1" "1.2.3"

echo "all release-tool tests passed"
