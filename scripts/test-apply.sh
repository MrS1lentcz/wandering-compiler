#!/usr/bin/env bash
# Apply-roundtrip harness: every fixture under
# `srcgo/domains/compiler/testdata/` is applied + rolled back on an
# ephemeral container of the named dialect + version.
#
# Usage:
#   scripts/test-apply.sh <dialect> <version>
#
# Arguments:
#   dialect — one of: postgres
#             (mysql lands with Layer C; sqlite/redis have no apply step today)
#   version — dialect major version, passed through to the Docker image tag
#             (e.g. "18" → postgres:18-alpine)
#
# Per-fixture version gate:
#   Each fixture dir may carry a `.min-pg-version` file holding a single
#   integer. Fixtures are skipped when the running version is lower —
#   used today by uuid_pk (PG 18+ for built-in uuidv7()).
#
# Exits non-zero on the first failing fixture. Container is torn down
# on exit (including Ctrl-C) via trap.

set -euo pipefail

if [[ $# -ne 2 ]]; then
    echo "usage: $0 <dialect> <version>" >&2
    exit 2
fi

DIALECT=$1
VERSION=$2
REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$REPO_ROOT"

command -v docker >/dev/null 2>&1 || { echo "test-apply: docker not found" >&2; exit 1; }

case "$DIALECT" in
    postgres)
        IMAGE="postgres:${VERSION}-alpine"
        VER_GATE_FILE=".min-pg-version"
        ;;
    *)
        echo "test-apply: unsupported dialect $DIALECT (only postgres today)" >&2
        exit 2
        ;;
esac

echo "test-apply: starting ephemeral $IMAGE"
CID=$(docker run --rm -d -e POSTGRES_PASSWORD=test "$IMAGE")
trap 'docker kill "$CID" >/dev/null 2>&1 || true' EXIT

# Wait for readiness. 60 seconds is generous for postgres:alpine cold start.
for _ in $(seq 1 60); do
    docker exec "$CID" pg_isready -U postgres -q 2>/dev/null && break
    sleep 1
done
docker exec "$CID" pg_isready -U postgres -q >/dev/null || {
    echo "test-apply: $IMAGE never became ready" >&2
    exit 1
}

# version_gate <fixture_dir> — returns 0 if we should run, 1 if skip.
# Reads optional .min-pg-version; skips when VERSION < required.
version_gate() {
    local dir=$1
    local gate_file="${dir}${VER_GATE_FILE}"
    if [[ ! -f "$gate_file" ]]; then
        return 0
    fi
    local min_version
    min_version=$(tr -d '[:space:]' < "$gate_file")
    if [[ -z "$min_version" ]]; then
        return 0
    fi
    if [[ "$VERSION" -lt "$min_version" ]]; then
        echo "  [skip: requires $DIALECT >= $min_version]"
        return 1
    fi
    return 0
}

# setup_db <db_name> — creates the test DB + extensions every fixture
# may reference. Idempotent on re-run (we use fresh DB names so won't
# re-run anyway).
setup_db() {
    local db=$1
    docker exec "$CID" psql -U postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE $db;" >/dev/null
    for ext in hstore citext pg_trgm; do
        docker exec "$CID" psql -U postgres -d "$db" -v ON_ERROR_STOP=1 \
            -c "CREATE EXTENSION IF NOT EXISTS $ext;" >/dev/null
    done
    docker exec "$CID" psql -U postgres -d "$db" -v ON_ERROR_STOP=1 \
        -c "CREATE SCHEMA IF NOT EXISTS reporting;" >/dev/null
}

# apply <db> <file> — streams a SQL file into psql under the DB.
apply() {
    local db=$1 file=$2
    docker exec -i "$CID" psql -U postgres -d "$db" -v ON_ERROR_STOP=1 < "$file" >/dev/null
}

# --- iter-1 fixtures: forward up → down → up roundtrip ---
for dir in srcgo/domains/compiler/testdata/*/; do
    name=$(basename "$dir")
    case "$name" in
        alter|alter_refuse) continue ;;
    esac
    echo "--- iter1 ($DIALECT $VERSION): $name ---"
    version_gate "$dir" || continue
    db="test_$name"
    setup_db "$db"
    for phase in up down up; do
        echo "  $phase"
        apply "$db" "${dir}expected.${phase}.sql"
    done
done

# --- alter fixtures: prev.up → diff.up → diff.down → prev.down ---
for dir in srcgo/domains/compiler/testdata/alter/*/; do
    name=$(basename "$dir")
    echo "--- alter ($DIALECT $VERSION): $name ---"
    version_gate "$dir" || continue
    db="test_alter_$name"
    setup_db "$db"

    # Compile prev.proto into its own migration pair so we can apply the
    # baseline before diffing.
    tmp="/tmp/wc-apply-${name}-${DIALECT}-${VERSION}"
    rm -rf "$tmp"
    (cd srcgo && COMPILER_CLASSIFICATION_DIR=../docs/classification \
        go run ./domains/compiler/cmd/cli generate --iteration-1 --no-applied-state \
            -I "$(pwd)/../proto" \
            -o "$tmp" \
            "$(pwd)/../${dir}prev.proto") >/dev/null
    prev_up=$(ls "$tmp"/migrations/*.up.sql)
    prev_down=$(ls "$tmp"/migrations/*.down.sql)

    echo "  prev.up"
    apply "$db" "$prev_up"
    echo "  diff.up"
    apply "$db" "${dir}expected.up.sql"
    echo "  diff.down"
    apply "$db" "${dir}expected.down.sql"
    echo "  prev.down"
    apply "$db" "$prev_down"

    rm -rf "$tmp"
done

echo "test-apply: $DIALECT $VERSION — all applicable fixtures applied + rolled back cleanly"
