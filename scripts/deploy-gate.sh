#!/bin/sh
# deploy-gate — decide whether a container cutover is safe to perform.
#
# Run BEFORE swapping containers. It compares the RUNNING instance's database
# schema version (GET /healthz, field schema.version) with the CANDIDATE
# image's schema ceiling label for the database dialect in use:
#
#   SQLite  — docker label co.thornotes.schema.max
#   MySQL   — docker label co.thornotes.schema.max.mysql
#
# Decisions:
#   db_version >  max_supported   →  REFUSE. Starting this image is a guaranteed
#                                    exit 1 at startup (internal/db/schema.go).
#   db_version <  max_supported   →  the image will migrate on start. REFUSE
#                                    unless a fresh database backup exists.
#   equal                         →  PROCEED.
#   anything unknown              →  REFUSE. Absent evidence is not permission:
#                                    an unreachable healthz, a missing label, or
#                                    an image that cannot be inspected all stop
#                                    the deploy.
#
# Usage:
#   scripts/deploy-gate.sh \
#     --image th0rn0/thornotes:<new tag> \
#     --driver mysql \
#     --healthz https://notes.example.com/healthz \
#     --backup /data/backups/thornotes-before-migration.sql
#
#   --image             image reference to inspect (must be present locally;
#                       docker pull it first). Required.
#   --driver            mysql or sqlite. Selects which schema label to compare.
#                       Required. Production runs MySQL.
#   --healthz           URL of the RUNNING instance's /healthz. Required.
#   --backup            path where the deploy procedure wrote its database
#                       backup. Required unless the decision is a plain
#                       no-migration proceed, but pass it always: the gate
#                       refuses to certify a migrating cutover without one.
#   --max-backup-age-min  a backup older than this many minutes is treated as
#                       missing (default 240).
#   --curl-timeout      seconds to wait for /healthz (default 10).
#
# Exit codes: 0 = safe to cut over, 1 = refused (reason printed, one line),
#             2 = usage error.
set -u

prog=$(basename "$0")

usage() {
    sed -n '2,45p' "$0" | sed 's/^# \{0,1\}//' >&2
    exit 2
}

die() { printf '%s: %s\n' "$prog" "$1" >&2; exit 2; }

IMAGE=""
DRIVER=""
HEALTHZ_URL=""
BACKUP=""
MAX_BACKUP_AGE_MIN=240
CURL_TIMEOUT=10

while [ $# -gt 0 ]; do
    case "$1" in
        --image)              IMAGE=${2-}; [ $# -ge 2 ] || die "--image needs a value"; shift ;;
        --driver)             DRIVER=${2-}; [ $# -ge 2 ] || die "--driver needs a value"; shift ;;
        --healthz)            HEALTHZ_URL=${2-}; [ $# -ge 2 ] || die "--healthz needs a value"; shift ;;
        --backup)             BACKUP=${2-}; [ $# -ge 2 ] || die "--backup needs a value"; shift ;;
        --max-backup-age-min) MAX_BACKUP_AGE_MIN=${2-}; [ $# -ge 2 ] || die "--max-backup-age-min needs a value"; shift ;;
        --curl-timeout)       CURL_TIMEOUT=${2-}; [ $# -ge 2 ] || die "--curl-timeout needs a value"; shift ;;
        -h|--help) usage ;;
        *) die "unknown argument: $1 (see --help)" ;;
    esac
    shift
done

[ -n "$IMAGE" ] || die "--image is required"
[ -n "$DRIVER" ] || die "--driver is required (mysql or sqlite)"
[ -n "$HEALTHZ_URL" ] || die "--healthz is required"

case "$DRIVER" in
    mysql)  LABEL='co.thornotes.schema.max.mysql' ;;
    sqlite) LABEL='co.thornotes.schema.max' ;;
    *)      die "--driver must be mysql or sqlite, got: $DRIVER" ;;
esac

refuse() { printf 'REFUSE: %s\n' "$1" >&2; exit 1; }

int_ok() { case "$1" in ''|*[!0-9]*) return 1 ;; *) return 0 ;; esac; }

# ---- 1. The running instance's database schema version ----------------------

body=$(curl -fsS --max-time "$CURL_TIMEOUT" "$HEALTHZ_URL" 2>/dev/null)
[ -n "$body" ] || refuse \
    "running instance /healthz at $HEALTHZ_URL is unreachable or returned nothing — fix or promote with the database state unknown, which is not permitted"

# The body also contains other objects (checks), so parse only from the schema
# block onward. Falls back to whatever the /healthz of this build emits
# ({"schema":{"version":N,"max_supported":M}}).
db_raw=$(printf '%s' "$body" | tr '{,' '\n' | grep '"version"' | head -1 | sed 's/[^0-9]//g')

if ! int_ok "$db_raw"; then
    refuse "/healthz at $HEALTHZ_URL returned no readable schema.version — treat the database version as unknown and stop"
fi

# ---- 2. The candidate image's schema ceiling --------------------------------

if ! command -v docker >/dev/null 2>&1; then
    refuse "docker is not available, so the image's $LABEL label cannot be read — inspect $IMAGE on the Docker host and re-run there"
fi

label_out=$(docker inspect --format "{{index .Config.Labels \"$LABEL\"}}" -- "$IMAGE" 2>&1)
if [ $? -ne 0 ]; then
    refuse "cannot inspect image $IMAGE locally: $label_out — docker pull it on the Docker host first, or correct the reference"
fi

max_supported=$label_out
if [ -z "$max_supported" ]; then
    refuse "image $IMAGE carries no $LABEL label — it predates PR #31 or was built without THORNOTES_SCHEMA_MAX, so its migration ceiling is unknown"
fi
case "$max_supported" in *'"'*) max_supported=$(printf '%s\n' "$max_supported" | tr -d '"') ;; esac
if ! int_ok "$max_supported"; then
    refuse "image $IMAGE label $LABEL is not a number ('$max_supported') — the migration ceiling is unknown"
fi

# ---- 3. Decide ---------------------------------------------------------------

if [ "$db_raw" -gt "$max_supported" ]; then
    refuse "database schema version $db_raw is newer than image $IMAGE supports ($max_supported): the container will exit at startup. Remedy: deploy a build with migration >= $db_raw (docker inspect the next image's label), or restore the database backup taken before migration $db_raw, then deploy this older image"
fi

if [ "$db_raw" -lt "$max_supported" ]; then
    if [ -z "$BACKUP" ]; then
        refuse "image $IMAGE (ceiling $max_supported) will migrate the database from version $db_raw on start, but no --backup path was given — take a database backup first and re-run"
    fi
    if [ ! -f "$BACKUP" ]; then
        refuse "image $IMAGE (ceiling $max_supported) will migrate the database from version $db_raw on start, but the backup '$BACKUP' does not exist — take the backup (see docs/operations.md 'Before deploying migrations') and re-run"
    fi
    if [ ! -s "$BACKUP" ]; then
        refuse "backup '$BACKUP' exists but is empty — an empty backup is not a rollback, take a real one before the migration runs"
    fi
    if [ -n "$(find -- "$BACKUP" -mmin "+$MAX_BACKUP_AGE_MIN" 2>/dev/null)" ]; then
        refuse "backup '$BACKUP' is older than ${MAX_BACKUP_AGE_MIN} minutes — a pre-migration backup must reflect the database you are about to migrate, take a fresh one"
    fi
    printf 'PROCEED: database version %s < image ceiling %s: migration will run on start; pre-migration backup at %s (age-checked, <= %s min) is in place\n' \
        "$db_raw" "$max_supported" "$BACKUP" "$MAX_BACKUP_AGE_MIN"
    exit 0
fi

printf 'PROCEED: database version %s equals image ceiling %s: no migration will run on start\n' \
    "$db_raw" "$max_supported"
exit 0
