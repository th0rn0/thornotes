#!/bin/sh
# host-backup.sh — watchtower safety-net: rolling pre-deploy backups on the
# Docker host (Thornotes ticket 602, brief 602-watchtower-safety-net-brief.md).
#
# Context: watchtower recreates th0rn0/thornotes:latest with nobody running
# scripts/deploy-gate.sh, so the only protection is a backup that predates any
# deploy. This script runs inside the Drone `safety-net` pipeline (docker
# driver runner on the production host, host docker socket mounted as a
# pipeline volume) from a Drone cron.
#
# Subcommands:
#   backup   mariadb-dump --single-transaction -> gzip, plus git bundle of the
#            notes tree (the tree is a git repo; a tarball of a live repo is
#            inconsistent, a bundle is the right artefact), then rotation.
#   verify   restore the newest dump into an ephemeral --tmpfs MariaDB built
#            from the SAME image as the production db; require the notes and
#            users row counts to equal production's; clone the newest bundle
#            and report its commit count.
#   status   rotated inventory + disk usage.
#
# Safety properties (read the body, not this comment, before trusting):
#   * container discovery is unique-match; two or zero candidates aborts;
#   * credentials are read inside the db container env, moved through docker
#     -e, and never echoed;
#   * writes: only new files under BACKUP_DIR (a host dir outside the db
#     volume and outside the notes tree), and scratch containers with one
#     reserved name that is removed even on error paths.
set -o pipefail 2>/dev/null || true

BACKUP_DIR="${SAFETY_BACKUP_DIR:-/servdata/thornotes/backups}"
KEEP_HOURLY="${KEEP_HOURLY:-168}"   # 168 hourly dumps and bundles = 7 days:
                                    # the brief requires "yesterday evening,
                                    # before the bad pull" (24 h) to exist
                                    # after any deploy, plus room for an
                                    # unattended weekend; each hourly sample
                                    # is small (see `status` for real sizes).
SCRATCH_DB="${SAFETY_SCRATCH_DB:-thornotes-scratch-restore}"
SCRATCH_PW="${SAFETY_SCRATCH_PW:-safetynet-scratch}"
GIT_IMAGE="${SAFETY_GIT_IMAGE:-alpine/git:2.43.4}"
BUSYBOX="${SAFETY_BUSYBOX_IMAGE:-busybox:1.36}"
TS=$(date -u '+%Y%m%dT%H%M%SZ')

log() { printf '[safety-net] %s %s\n' "$TS" "$*"; }
die() { printf '[safety-net] FATAL: %s\n' "$*" >&2; exit 1; }

docker ps >/dev/null 2>&1 || die "docker CLI/socket unavailable"

# ---------- discovery: unique match each time, never a guess -------------------
DB=$(docker ps --format '{{.Names}}\t{{.Image}}' | awk -F'\t' \
  'tolower($2) ~ /maria|mysql/ && $1 !~ /thornotes-smoke|thornotes-scratch|thornotes-backup-helper/ {print $1}' | head -1)
APP=$(docker ps --format '{{.Names}}\t{{.Image}}' | awk -F'\t' \
  'tolower($2) ~ /th0rn0\/thornotes/ && $1 !~ /thornotes-smoke|thornotes-scratch|thornotes-backup-helper|ci-/ {print $1}' | head -1)
[ -n "$DB" ]  || die "no mysql/mariadb container running"
[ -n "$APP" ] || die "no thornotes app container running"
DB_COUNT=$(docker ps --format '{{.Names}}\t{{.Image}}' | awk -F'\t' \
  'tolower($2) ~ /maria|mysql/ && $1 !~ /thornotes-smoke|thornotes-scratch|thornotes-backup-helper/ {print $1}' | wc -l)
APP_COUNT=$(docker ps --format '{{.Names}}\t{{.Image}}' | awk -F'\t' \
  'tolower($2) ~ /th0rn0\/thornotes/ && $1 !~ /thornotes-smoke|thornotes-scratch|thornotes-backup-helper|ci-/ {print $1}' | wc -l)
[ "$DB_COUNT"  = "1" ] || die "expected exactly 1 mysql/mariadb container, found $DB_COUNT"
[ "$APP_COUNT" = "1" ] || die "expected exactly 1 thornotes app container, found $APP_COUNT"
DB_IMAGE=$(docker inspect "$DB"  --format '{{.Config.Image}}')
APP_IMAGE=$(docker inspect "$APP" --format '{{.Config.Image}}')
log "discovered: db=$DB ($DB_IMAGE)  app=$APP ($APP_IMAGE)"

CREDS=$(docker exec "$DB" sh -c '
  c="" ; d=""
  for x in mariadb-admin mysqladmin; do command -v "$x" >/dev/null 2>&1 && { c=$x; break; }; done
  for x in mariadb-dump mysqldump;    do command -v "$x" >/dev/null 2>&1 && { d=$x; break; }; done
  [ -n "$c" ] && [ -n "$d" ] || { echo NOCLIENT; exit 0; }
  for v in MYSQL_ROOT_PASSWORD MARIADB_ROOT_PASSWORD; do
    eval "test -n \"\${$v:-}\" && { echo \"CLIENT=$c DUMP=$d PWVAR=$v\"; exit 0; }"
  done
  echo NOPW')
case "$CREDS" in
  CLIENT=*) : ;;
  NOPW)     die "db container exposes neither MYSQL_ROOT_PASSWORD nor MARIADB_ROOT_PASSWORD" ;;
  NOCLIENT) die "db container has no mariadb/mysql client binaries" ;;
  *)        die "could not read db credential layout ('$CREDS')" ;;
esac
CLIENT=$(printf '%s\n' "$CREDS" | sed -n 's/.*CLIENT=\([^ ]*\).*/\1/p')
DUMPBIN=$(printf '%s\n' "$CREDS" | sed -n 's/.*DUMP=\([^ ]*\).*/\1/p')
PWVAR=$(printf '%s\n'  "$CREDS" | sed -n 's/.*PWVAR=\([^ ]*\).*/\1/p')
PW=$(docker exec "$DB" sh -c "printf '%s' \"\${$PWVAR:-}\"") || true
[ -n "$PW" ] || die "db root password is empty; refusing to dump"
log "creds: root password taken from $PWVAR of the db container env (never printed); client=$CLIENT dump=$DUMPBIN"

# database schemas: everything except the system schemas; require exactly one
# thornotes-like database.
DBLIST=$(docker run --rm -e DBPW="$PW" -e CLIENT="$CLIENT" \
  --network "container:$DB" "$DB_IMAGE" \
  sh -c '$CLIENT -uroot -p"$DBPW" -NBe "SHOW DATABASES"' 2>/dev/null) \
  || die "could not list databases"
DBNAME=$(printf '%s\n' "$DBLIST" | awk '
  $0 ~ /^(information_schema|performance_schema|mysql|sys)$/ { next }
  $0 ~ /^thornotes/ { print }')
[ -n "$DBNAME" ] || die "no thornotes schema found among: $(printf '%s' "$DBLIST" | tr '\n' ' ')"
[ "$(printf '%s\n' "$DBNAME" | wc -l)" = "1" ] || die "more than one thornotes schema: $(printf '%s' "$DBNAME" | tr '\n' ' ')"
log "database: $DBNAME"

# notes-tree bind source on the app container: prefer a bind sourced under
# /servdata (documented /servdata/thornotes/notes), else the single rw bind.
MOUNTS=$(docker inspect "$APP" --format '{{range .Mounts}}{{.Type}}|{{.Source}}|{{.Destination}}
{{end}}')
log "app mounts: $(printf '%s\n' "$MOUNTS" | tr '\n' ' ' | sed 's/  */ /g')"
NOTES_SRC=$(printf '%s\n' "$MOUNTS" | awk -F'|' '$1=="bind" && $3 ~ /data/ {print $2}' | head -1)
[ -n "$NOTES_SRC" ] || die "no bind-mounted data dir found on app container $APP"
log "notes tree: $NOTES_SRC"

DUMP_FILE="thornotes-db-$TS.sql.gz"
BUNDLE_FILE="notes-$TS.bundle"
DUMP_TMP=".part-$DUMP_FILE"
BUNDLE_TMP=".part-$BUNDLE_FILE"

COUNT_SQL=$(printf "SELECT 'notes', COUNT(*) FROM %s.notes UNION ALL SELECT 'users', COUNT(*) FROM %s.users" "$DBNAME" "$DBNAME")

prep_dir() {
  docker run --rm -v "$BACKUP_DIR:/out" "$BUSYBOX" mkdir -p /out
}
counts() { # $1 = container (scratch) or empty -> producer via docker run, $2 = client, $3 = pw value
  docker exec -e DBPW="$3" -e CLIENT="$2" -e CSQL="$COUNT_SQL" \
    "$1" sh -c '$CLIENT -uroot -p"$DBPW" -NBe "$CSQL"' 2>/dev/null || true
}

case "${1:-help}" in

# ================================= backup ======================================
backup)
  prep_dir
  log "target: $DUMP_FILE + $BUNDLE_FILE under $BACKUP_DIR; keeping newest $KEEP_HOURLY of each"

  # dump through a helper sharing the db container network namespace, the
  # password handed over via -e; the part file is committed after integrity
  # checking so a half-written dump never carries the canonical name.
  docker run --rm -v "$BACKUP_DIR:/out" -e DBPW="$PW" -e DUMPBIN="$DUMPBIN" \
    -e DBNAME="$DBNAME" --network "container:$DB" "$DB_IMAGE" \
    sh -c '$DUMPBIN --single-transaction --hex-blob --routines --triggers --events \
      --databases "$DBNAME" -uroot -p"$DBPW" | gzip -c > /out/'"$DUMP_TMP" \
    || die "mariadb-dump through helper failed"
  docker run --rm -v "$BACKUP_DIR:/out" "$BUSYBOX" sh -c '
    test -s /out/'"$DUMP_TMP"' || { echo "dump part file empty"; exit 1; }
    gzip -t /out/'"$DUMP_TMP"' || exit 1
    mv /out/'"$DUMP_TMP"' /out/'"$DUMP_FILE" \
    || die "dump integrity check failed; fragment left for inspection"

  # git bundle of the notes tree; the repo itself is mounted READ-ONLY.
  docker run --rm -v "$NOTES_SRC:/notes:ro" -v "$BACKUP_DIR:/out" -w /notes \
    "$GIT_IMAGE" bundle create "/out/$BUNDLE_TMP" --all >/dev/null \
    || die "git bundle create failed (src=$NOTES_SRC)"
  docker run --rm -v "$NOTES_SRC:/notes:ro" -v "$BACKUP_DIR:/out" -w /notes \
    "$GIT_IMAGE" bundle verify "/out/$BUNDLE_TMP" >/dev/null \
    || die "git bundle verify failed on fragment"
  docker run --rm -v "$BACKUP_DIR:/out" "$BUSYBOX" mv "/out/$BUNDLE_TMP" "/out/$BUNDLE_FILE"

  # rotation: keep the newest KEEP_HOURLY files of each kind.
  docker run --rm -v "$BACKUP_DIR:/out" "$BUSYBOX" sh -c '
    cd /out
    cut=$(( '"$KEEP_HOURLY"' + 1 ))
    for kind in "thornotes-db-*.sql.gz" "notes-*.bundle"; do
      for f in $(ls -1t $kind 2>/dev/null | tail -n +$cut); do
        rm -f -- "$f"
      done
    done
    true'
  KEEPSUM=$(docker run --rm -v "$BACKUP_DIR:/out" "$BUSYBOX" sh -c '
    cd /out
    printf "%s|%s|%s" \
      "$(ls -1 thornotes-db-*.sql.gz 2>/dev/null | wc -l)" \
      "$(ls -1 notes-*.bundle 2>/dev/null | wc -l)" \
      "$(du -sh /out 2>/dev/null | cut -f1)"') || KEEPSUM="?"
  DUMPS=$(printf '%s' "$KEEPSUM" | cut -d "|" -f1)
  BUNDLES=$(printf '%s' "$KEEPSUM" | cut -d "|" -f2)
  DU=$(printf '%s' "$KEEPSUM" | cut -d "|" -f3)
  DUMP_KB=$(docker run --rm -v "$BACKUP_DIR:/out" "$BUSYBOX" du -k "/out/$DUMP_FILE" | cut -f1)
  BUNDLE_KB=$(docker run --rm -v "$BACKUP_DIR:/out" "$BUSYBOX" du -k "/out/$BUNDLE_FILE" | cut -f1)
  log "rotation: dumps=$DUMPS (cap $KEEP_HOURLY) bundles=$BUNDLES (cap $KEEP_HOURLY) disk=$DU"
  log "BACKUP OK: dump=$DUMP_FILE ${DUMP_KB}KB bundle=$BUNDLE_FILE ${BUNDLE_KB}KB db=$DB dumpdir=$BACKUP_DIR"

  printf '%s BACKUP OK db=%s dump=%s bundle=%s\n' "$TS" "$DB" "$DUMP_FILE" "$BUNDLE_FILE" \
    | docker run --rm -i -v "$BACKUP_DIR:/out" "$BUSYBOX" sh -c 'cat >> /out/backup.log'
  ;;

# ================================= verify ======================================
verify)
  NEWEST_DUMP=$(docker run --rm -v "$BACKUP_DIR:/out" "$BUSYBOX" sh -c \
    'cd /out && ls -1t thornotes-db-*.sql.gz 2>/dev/null | head -1' | tr -d '\n')
  NEWEST_BUNDLE=$(docker run --rm -v "$BACKUP_DIR:/out" "$BUSYBOX" sh -c \
    'cd /out && ls -1t notes-*.bundle 2>/dev/null | head -1' | tr -d '\n')
  [ -n "$NEWEST_DUMP" ]   || die "no dump file in $BACKUP_DIR"
  [ -n "$NEWEST_BUNDLE" ] || die "no bundle file in $BACKUP_DIR"
  log "verify: newest dump=$NEWEST_DUMP newest bundle=$NEWEST_BUNDLE"

  # fresh production counts, read minutes after the dump (same build) so the
  # comparison is exact.
  PROD_COUNTS=$(docker run --rm -e DBPW="$PW" -e CLIENT="$CLIENT" -e CSQL="$COUNT_SQL" \
    --network "container:$DB" "$DB_IMAGE" \
    sh -c '$CLIENT -uroot -p"$DBPW" -NBe "$CSQL"' 2>/dev/null) \
    || PROD_COUNTS=""
  [ -n "$PROD_COUNTS" ] || die "could not read production row counts"
  log "counts production: $(printf '%s' "$PROD_COUNTS" | tr '\n' '; ')"

  # scratch db from the SAME image, tmpfs only, one reserved name, always removed.
  docker rm -f "$SCRATCH_DB" >/dev/null 2>&1 || true
  cleanup() { docker rm -f "$SCRATCH_DB" >/dev/null 2>&1 || true; }
  trap cleanup EXIT INT TERM
  docker run -d --name "$SCRATCH_DB" -e MARIADB_ROOT_PASSWORD="$SCRATCH_PW" \
    --tmpfs /var/lib/mysql "$DB_IMAGE" >/dev/null || die "scratch db could not start"
  waited=0
  until docker exec "$SCRATCH_DB" mariadb-admin ping -uroot -p"$SCRATCH_PW" 2>/dev/null | grep -q alive; do
    waited=$((waited + 1))
    [ "$waited" -lt 60 ] || die "scratch db never came up"
    sleep 1
  done
  log "scratch db up from the same image ($DB_IMAGE), tmpfs only — nothing of production touched"

  docker run --rm -v "$BACKUP_DIR:/srv:ro" "$BUSYBOX" sh -c 'gzip -dc /srv/'"$NEWEST_DUMP" \
    | docker exec -i -e DBPW="$SCRATCH_PW" -e CLIENT="$CLIENT" "$SCRATCH_DB" \
        sh -c '$CLIENT -uroot -p"$DBPW"' \
    || die "restoring $NEWEST_DUMP into scratch failed"
  log "restore complete: $NEWEST_DUMP -> $SCRATCH_DB (tmpfs)"

  SCRATCH_COUNTS=$(counts "$SCRATCH_DB" "$CLIENT" "$SCRATCH_PW")
  [ -n "$SCRATCH_COUNTS" ] || die "could not read scratch row counts"
  log "counts scratch:    $(printf '%s' "$SCRATCH_COUNTS" | tr '\n' '; ')"

  if [ "$PROD_COUNTS" = "$SCRATCH_COUNTS" ]; then
    log "VERIFY OK: restored '$NEWEST_DUMP' matches production row counts: notes=$(printf '%s' "$PROD_COUNTS" | head -1 | cut -f2) users=$(printf '%s' "$PROD_COUNTS" | tail -1 | cut -f2)"
  else
    log "VERIFY MISMATCH: restored counts are not equal to production — treat this backup as failing"
    exit 1
  fi

  # git bundle restorability: clone it and report commit count + head.
  BUNDLE_PROOF=$(docker run --rm -v "$BACKUP_DIR:/srv:ro" --entrypoint sh "$GIT_IMAGE" -c '
    git clone -q /srv/'"$NEWEST_BUNDLE"' /tmp/clone >/dev/null 2>&1 || { echo CLONEFAIL; exit 0; }
    printf "commits=%s head=%s" "$(git -C /tmp/clone rev-list --count HEAD)" "$(git -C /tmp/clone log -1 --format=%H)" \
      || { echo CLONEFAIL; exit 0; }')
  case "$BUNDLE_PROOF" in
    CLONEFAIL) die "git bundle did not clone: $NEWEST_BUNDLE" ;;
    commits=*) log "BUNDLE OK: $BUNDLE_PROOF" ;;
    *)         die "git bundle clone gave no evidence ('$BUNDLE_PROOF')" ;;
  esac
  log "VERIFY DONE ($NEWEST_DUMP + $NEWEST_BUNDLE)"
  ;;

# ================================= status ======================================
status)
  docker run --rm -v "$BACKUP_DIR:/out" "$BUSYBOX" sh -c '
    cd /out
    echo "dump files:    $(ls -1 thornotes-db-*.sql.gz 2>/dev/null | wc -l) (cap '"$KEEP_HOURLY"')"
    echo "bundle files:  $(ls -1 notes-*.bundle 2>/dev/null | wc -l) (cap '"$KEEP_HOURLY"')"
    echo "disk used:     $(du -sh /out 2>/dev/null | cut -f1)"
    ls -1t thornotes-db-*.sql.gz 2>/dev/null | head -3 | sed "s/^/  newest: /"
    ls -1t notes-*.bundle 2>/dev/null | head -3 | sed "s/^/  newest bundle: /"
    true'
  ;;

*) sed -n '2,17p' "$0" ;;
esac
