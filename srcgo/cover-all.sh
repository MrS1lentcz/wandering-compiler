#!/usr/bin/env bash
# Collect cross-package coverage across every test package.
# Usage: ./cover-all.sh [> profile] — writes a merged coverage profile
# suitable for `go tool cover -func=...` or -html=....
#
# Each package gets its own profile with -coverpkg=./domains/compiler/...
# so per-package test binaries see shared-package statements. The files
# are merged by taking MAX count per (file:range) tuple (the standard
# coverage-merging semantics go tool cover uses internally).
set -euo pipefail
cd "$(dirname "$0")"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

pkgs=$(go list ./... | grep -v '/pb/' | grep -v '/cmd/cli')
i=0
for p in $pkgs; do
    go test -coverpkg=./domains/compiler/... -coverprofile="$TMP/$i.out" "$p" >/dev/null 2>&1 || true
    i=$((i + 1))
done

# Merge: emit header once, then dedup-max entries across profiles.
echo "mode: set"
for f in "$TMP"/*.out; do
    tail -n +2 "$f" 2>/dev/null || true
done | awk '{
    key=$1" "$2
    if ($NF > seen[key]) { seen[key] = $NF }
    loc[key] = $1
    stmts[key] = $2
} END {
    for (k in loc) printf "%s %s %d\n", loc[k], stmts[k], seen[k]
}'
