#!/bin/sh
# schema-publish-gate — decide whether the candidate image may be tagged :latest.
#
# Production runs th0rn0/thornotes:latest under watchtower: every green `main`
# build is pulled and restarted with no human in the loop. The startup preflight
# (internal/db/schema.go) refuses to run against a database that is ahead of the
# binary, but that refusal happens AFTER the swap, on the live instance, at
# whatever time the publish lands. This gate asks the same question earlier:
# before the pipeline tags :latest, compare the live database's schema version
# with the ceiling the candidate image is about to be built with.
#
# Refusals (exit 1), and they are the point:
#   - the live database is ahead of the candidate ceiling
#   - /healthz is unreachable or returns nothing
#   - /healthz has no readable schema block
#   - the schema version is not an unquoted integer
# Unknown is a refusal, never a pass: a gate that treats "could not ask" as
# "fine" is decoration.
#
# Only :latest is gated. Tag builds and branch builds must not depend on
# production being up; wire this into the pipeline step that pushes :latest,
# and only that step (see .drone.yml).
#
# Usage:
#   sh scripts/schema-publish-gate.sh \
#     --url https://notes.th0rn0.co.uk/healthz \
#     --engine mysql \
#     --max 7 \
#     [--timeout 10]
#
#   --url      /healthz of the RUNNING production instance. Required.
#   --engine   mysql or sqlite; names the ceiling being compared. Required.
#              Production runs MySQL.
#   --max      the candidate image's schema ceiling for that engine. In CI this
#              must equal the THORNOTES_SCHEMA_MAX_MYSQL build arg in
#              .drone.yml (TestDrone_PublishGateCeilingMatchesMigrations fails
#              the build when they drift). Required.
#   --timeout  seconds to wait for /healthz (default 10).
#
# Exit codes: 0 = safe to tag latest, 1 = refused (one log line says why),
#             2 = usage error.
#
# The version comparison lives between the "decide: begin" / "decide: end"
# markers. scripts/schema-publish-gate.test.sh strips that block to prove its
# table still fails when the gate stops comparing — do not edit the markers.
set -u

prog=$(basename "$0")

URL=""
ENGINE=""
CANDIDATE_MAX=""
TIMEOUT=10

die() { printf '%s: %s\n' "$prog" "$1" >&2; exit 2; }
refuse() { printf '%s\n' "$1"; exit 1; }
int_ok() { case "$1" in ''|*[!0-9]*) return 1 ;; *) return 0 ;; esac; }

while [ $# -gt 0 ]; do
    case "$1" in
        --url)     [ $# -ge 2 ] || die "--url needs a value"; URL=$2; shift ;;
        --engine)  [ $# -ge 2 ] || die "--engine needs a value"; ENGINE=$2; shift ;;
        --max)     [ $# -ge 2 ] || die "--max needs a value"; CANDIDATE_MAX=$2; shift ;;
        --timeout) [ $# -ge 2 ] || die "--timeout needs a value"; TIMEOUT=$2; shift ;;
        -h|--help) sed -n '2,42p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) die "unknown argument: $1 (see --help)" ;;
    esac
    shift
done

[ -n "$URL" ]           || die "--url is required"
[ -n "$CANDIDATE_MAX" ] || die "--max is required"
case "$ENGINE" in
    mysql)  engine_name="MySQL"  ;;
    sqlite) engine_name="SQLite" ;;
    *)      die "--engine must be mysql or sqlite, got: $ENGINE" ;;
esac
int_ok "$TIMEOUT" || die "--timeout must be a number, got: $TIMEOUT"

if ! int_ok "$CANDIDATE_MAX"; then
    refuse "schema-publish-gate: REFUSE -- candidate $engine_name ceiling '$CANDIDATE_MAX' is not a number vs live database version unknown: refusing to tag latest (unknown is a refusal, never a pass)"
fi

# ---- 1. Ask the live instance -------------------------------------------------

body=$(curl -fsS --max-time "$TIMEOUT" "$URL" 2>/dev/null)
if [ -z "$body" ]; then
    refuse "schema-publish-gate: REFUSE -- candidate $engine_name ceiling $CANDIDATE_MAX vs live database version unknown: $URL unreachable or returned nothing after ${TIMEOUT}s; refusing to tag latest (unknown is a refusal, never a pass)"
fi

# The body also carries other objects (checks, status), so parse only the lines
# that survive from the schema object onward. Only an unquoted integer counts
# as a version: anything else is unknown, and unknown refuses. schema.
# max_supported (the live build's own ceiling) is required too, so a decoy JSON
# page that happens to carry a "version" cannot satisfy the gate.
db_raw=$(printf '%s' "$body" | tr '{,' '\n\n' | grep '"version"' | head -1 | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
live_max=$(printf '%s' "$body" | tr '{,' '\n\n' | grep '"max_supported"' | head -1 | sed -n 's/.*"max_supported"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')

if ! int_ok "$db_raw"; then
    refuse "schema-publish-gate: REFUSE -- candidate $engine_name ceiling $CANDIDATE_MAX vs live database version unknown: $URL returned no readable schema.version (missing schema block or non-numeric); refusing to tag latest (unknown is a refusal, never a pass)"
fi
if ! int_ok "$live_max"; then
    refuse "schema-publish-gate: REFUSE -- candidate $engine_name ceiling $CANDIDATE_MAX vs live database version $db_raw: $URL returned no readable schema.max_supported; refusing to tag latest (unknown is a refusal, never a pass)"
fi

# ---- decide: begin (negative-control marker, do not edit) ----
if [ "$db_raw" -gt "$CANDIDATE_MAX" ]; then
    refuse "schema-publish-gate: REFUSE -- candidate $engine_name ceiling $CANDIDATE_MAX vs live database version $db_raw: the live database is ahead of the candidate image; refusing to tag latest (watchtower would deploy an image that exits at startup; land a build whose migrations reach version $db_raw first)"
fi
# ---- decide: end ----

if [ "$db_raw" -lt "$CANDIDATE_MAX" ]; then
    printf 'schema-publish-gate: PROCEED -- candidate %s ceiling %s vs live database version %s (live build max_supported %s): the candidate will migrate the database on start; tagging latest may proceed\n' \
        "$engine_name" "$CANDIDATE_MAX" "$db_raw" "$live_max"
    exit 0
fi

printf 'schema-publish-gate: PROCEED -- candidate %s ceiling %s vs live database version %s (live build max_supported %s): tagging latest may proceed\n' \
    "$engine_name" "$CANDIDATE_MAX" "$db_raw" "$live_max"
exit 0
