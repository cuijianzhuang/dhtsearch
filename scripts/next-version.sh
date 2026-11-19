#!/usr/bin/env bash
# next-version.sh — compute the next automatic release version.
#
# Reads tag names (one per line) from stdin, or from `git tag -l` when
# stdin is a terminal. A piped-but-empty stdin is authoritative (no tags).
# Only strict vX.Y.Z tags count; prerelease
# or otherwise suffixed tags (v0.2.0-rc.1, v1, nightly-…) are ignored.
# The next version is the greatest such tag with its patch bumped by one.
# With no usable tag at all, the first automatic release is v0.1.0.
#
# Usage:
#   git tag -l | scripts/next-version.sh
#   scripts/next-version.sh < tags.txt
#
# Prints the version (e.g. v0.1.3) on stdout.
# Test it with scripts/test-release-tools.sh.
set -euo pipefail

tags=""
if [ ! -t 0 ]; then
    tags="$(cat)"
else
    tags="$(git tag -l 2>/dev/null || true)"
fi

latest="$(printf '%s\n' "$tags" \
    | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' \
    | sort -V \
    | tail -n 1 || true)"

if [ -z "$latest" ]; then
    echo "v0.1.0"
    exit 0
fi

version="${latest#v}"
major="${version%%.*}"
rest="${version#*.}"
minor="${rest%%.*}"
patch="${rest#*.}"
echo "v${major}.${minor}.$((patch + 1))"
