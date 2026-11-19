#!/usr/bin/env bash
# release-scope.sh — decide whether a set of changed files is a release.
#
# Used by .github/workflows/auto-release.yml: a push to main only gets an
# automatic release when it touches business code. Docs, scripts and CI
# changes alone never release; business code mixed with docs does.
#
# Usage:
#   git diff --name-only <before> <after> | scripts/release-scope.sh
#   scripts/release-scope.sh <file> [<file> ...]
#
# Prints exactly "true" (release) or "false" (skip) on stdout and always
# exits 0, so callers compare the output instead of juggling exit codes.
# Test it with scripts/test-release-tools.sh.
set -euo pipefail

is_business_file() {
    case "$1" in
        # Backend: Go sources and the module pins that change the build.
        server/*.go) return 0 ;;
        server/go.mod|server/go.sum) return 0 ;;
        # Frontend: routes, components, shared lib, static assets and the
        # build inputs that change the shipped bundle.
        web/app/*) return 0 ;;
        web/components/*) return 0 ;;
        web/lib/*) return 0 ;;
        web/public/*) return 0 ;;
        web/package.json|web/package-lock.json) return 0 ;;
        web/next.config.*) return 0 ;;
        web/tsconfig*.json) return 0 ;;
        web/postcss.config.*) return 0 ;;
        web/*.css) return 0 ;;
        # Everything else — *.md, docs/, scripts/, .github/, env.example,
        # deploy.sh, LICENSE — never triggers a release on its own.
        *) return 1 ;;
    esac
}

files=()
if [ $# -gt 0 ]; then
    files=("$@")
else
    while IFS= read -r line; do
        [ -n "$line" ] && files+=("$line")
    done
fi

for f in ${files[@]+"${files[@]}"}; do
    if is_business_file "$f"; then
        echo "true"
        exit 0
    fi
done
echo "false"
