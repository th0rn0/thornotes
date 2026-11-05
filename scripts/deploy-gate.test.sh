#!/bin/sh
# deploy-gate.test.sh — scripted check of scripts/deploy-gate.sh.
#
# Runs every decision the gate can make against real inputs where possible:
# the image-label path is always exercised with `docker inspect` on real images
# (a pulled th0rn0/thornotes image for the label, and an image that carries no
# schema label at all for the unknown-ceiling refusal). The running instance's
# /healthz is a tiny stub HTTP server when the real instance cannot report the
# version a case needs — e.g. a database stamped AHEAD of the binary, which the
# real binary refuses at startup and therefore cannot serve from /healthz.
#
# Usage, on a Docker host:
#   scripts/deploy-gate.test.sh
#
# Env:
#   THORNOTES_IMAGE   image to inspect for the real label
#                     (default th0rn0/thornotes:latest; pulled if missing)
#   MYSQL_LABEL_IMG   image expected to carry NO schema label
#                     (default mariadb:11; pulled if missing)
#
# Exit 0 when every case matches, 1 on the first mismatch (diff printed).

set -u

here=$(cd "$(dirname "$0")" && pwd)
gate="$here/deploy-gate.sh"
[ -x "$gate" ] || { echo "FAIL: $gate missing or not executable"; exit 1; }

THORNOTES_IMAGE=${THORNOTES_IMAGE:-th0rn0/thornotes:latest}
MYSQL_LABEL_IMG=${MYSQL_LABEL_IMG:-mariadb:11}
PY=${PY:-python3}

fail=0
note() { printf '\n== %s\n' "$*"; }
ok() { printf 'PASS: %s\n' "$*"; }
command -v docker >/dev/null 2>&1 || { echo "FAIL: docker not available"; exit 1; }
command -v "$PY" >/dev/null 2>&1 || { echo "FAIL: $PY not available (stub healthz dependency)"; exit 1; }

for img in "$THORNOTES_IMAGE" "$MYSQL_LABEL_IMG"; do
    docker image inspect -- "$img" >/dev/null 2>&1 || docker pull -- "$img" >/dev/null 2>&1 \
        || { echo "FAIL: cannot obtain image $img"; exit 1; }
done

real_label=$(docker inspect --format '{{index .Config.Labels "co.thornotes.schema.max.mysql"}}' -- "$THORNOTES_IMAGE")
[ -n "$real_label" ] || { echo "FAIL: $THORNOTES_IMAGE carries no mysql schema label"; exit 1; }
echo "real label from $THORNOTES_IMAGE: co.thornotes.schema.max.mysql = $real_label"
echo "(cases below deliberately use versions on both sides of $real_label so no case depends on the number moving)"

# stub_healthz <port> <version> [max_supported]
STUB_LOG=""
stub_healthz() {
    port=$1; version=$2; maxsup=${3:-$2}
    STUB_LOG=$(mktemp)
    "$PY" -c '
import http.server, json, sys
port = int(sys.argv[1]); version = int(sys.argv[2]); maxsup = int(sys.argv[3])
body = json.dumps({"status": "ok", "checks": {"jwt": "ok", "notes_root": "ok"},
        "schema": {"version": version, "max_supported": maxsup}}).encode()
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
' "$port" "$version" "$maxsup" >/dev/null 2>"$STUB_LOG" &
    STUB_PID=$!
    ready=0
    for _ in $(seq 1 40); do
        if curl -fsS --max-time 1 "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then ready=1; break; fi
        kill -0 "$STUB_PID" 2>/dev/null || break
        sleep 0.25
    done
    if [ "$ready" -ne 1 ]; then
        printf 'FAIL: stub healthz on port %s never became ready\n  stub stderr: %s\n' "$port" "$(cat "$STUB_LOG" 2>/dev/null)"
        exit 1
    fi
}

case_run() {
    name=$1; expect_rc=$2; expect_msg=$3
    shift 3
    set +e
    out=$("$@" 2>&1)
    rc=$?
    set -e
    if [ "$rc" -ne "$expect_rc" ]; then
        printf 'FAIL: %s — exit %s, expected %s\n  output: %s\n' "$name" "$rc" "$expect_rc" "$out"
        fail=1
    elif [ -z "$expect_msg" ]; then
        ok "$name (exit $rc)"
    elif printf '%s' "$out" | grep -qF -- "$expect_msg"; then
        ok "$name (exit $rc, message matched)"
    else
        printf 'FAIL: %s — expected message fragment "%s", got: %s\n' "$name" "$expect_msg" "$out"
        fail=1
    fi
    printf '  gate output: %s\n' "$out"
}

backup_dir=$(mktemp -d)
trap 'kill "${STUB_PID:-}" 2>/dev/null; rm -rf "$backup_dir"' EXIT INT TERM

note "1. PROCEED — database version equals the image ceiling (real label: $real_label)"
stub_healthz 18081 "$real_label"
case_run "equal versions" 0 "PROCEED: database version $real_label equals image ceiling $real_label" \
    "$gate" --image "$THORNOTES_IMAGE" --driver mysql \
    --healthz http://127.0.0.1:18081/healthz
kill "$STUB_PID" 2>/dev/null

note "2. REFUSE — database AHEAD of the image (guaranteed startup exit); must be refused before any container starts"
stub_healthz 18082 $((real_label + 1))
case_run "too-new database" 1 \
    "REFUSE: database schema version $((real_label + 1)) is newer than image $THORNOTES_IMAGE supports ($real_label)" \
    "$gate" --image "$THORNOTES_IMAGE" --driver mysql \
    --healthz http://127.0.0.1:18082/healthz --backup /nonexistent
kill "$STUB_PID" 2>/dev/null

note "3. REFUSE — image will migrate (version < ceiling) and no --backup given"
stub_healthz 18083 $((real_label - 1))
case_run "missing backup (no path)" 1 "no --backup path was given" \
    "$gate" --image "$THORNOTES_IMAGE" --driver mysql \
    --healthz http://127.0.0.1:18083/healthz

note "4. REFUSE — image will migrate and the --backup path does not exist"
case_run "missing backup (absent file)" 1 "the backup '/does/not/exist.sql' does not exist" \
    "$gate" --image "$THORNOTES_IMAGE" --driver mysql \
    --healthz http://127.0.0.1:18083/healthz --backup /does/not/exist.sql

note "5. PROCEED — migration will run, fresh backup present (age within budget)"
printf '%s\n' "-- real dump body --" > "$backup_dir/thornotes-before-migration.sql"
printf '%s\n' "-- stale dump body --" > "$backup_dir/stale.sql"
touch -d "-10 minutes" "$backup_dir/stale.sql"
case_run "fresh backup" 0 \
    "PROCEED: database version $((real_label - 1)) < image ceiling $real_label: migration will run on start" \
    "$gate" --image "$THORNOTES_IMAGE" --driver mysql \
    --healthz http://127.0.0.1:18083/healthz --backup "$backup_dir/thornotes-before-migration.sql" \
    --max-backup-age-min 60

note "6. REFUSE — backup exists but is older than the age budget"
case_run "stale backup" 1 "backup '$backup_dir/stale.sql' is older than 1 minutes" \
    "$gate" --image "$THORNOTES_IMAGE" --driver mysql \
    --healthz http://127.0.0.1:18083/healthz --backup "$backup_dir/stale.sql" \
    --max-backup-age-min 1
kill "$STUB_PID" 2>/dev/null

note "7. REFUSE — /healthz unreachable (absent evidence is not permission); no container is started"
case_run "unreachable healthz" 1 "is unreachable or returned nothing" \
    "$gate" --image "$THORNOTES_IMAGE" --driver mysql \
    --healthz http://127.0.0.1:18099/healthz

note "8. REFUSE — candidate image carries no schema label (real image: $MYSQL_LABEL_IMG)"
stub_healthz 18084 "$real_label"
case_run "label absent" 1 "carries no co.thornotes.schema.max.mysql label" \
    "$gate" --image "$MYSQL_LABEL_IMG" --driver mysql \
    --healthz http://127.0.0.1:18084/healthz
kill "$STUB_PID" 2>/dev/null

note "9. REFUSE — candidate image cannot be inspected (nonexistent reference)"
stub_healthz 18084 "$real_label"
case_run "unknown image" 1 "cannot inspect image" \
    "$gate" --image th0rn0/no-such-image:never --driver mysql \
    --healthz http://127.0.0.1:18084/healthz
kill "$STUB_PID" 2>/dev/null

note "10. REFUSE — /healthz reachable but schema block unparseable"
tag_port=18085
"$PY" -c '
import http.server, sys
port = int(sys.argv[1])
body = b"<html><body>proxy error page</body></html>"
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/html")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
' "$tag_port" >/dev/null 2>&1 &
STUB_PID=$!
ready=0
for _ in $(seq 1 40); do
    curl -fsS --max-time 1 "http://127.0.0.1:$tag_port/healthz" >/dev/null 2>&1 && { ready=1; break; }
    kill -0 "$STUB_PID" 2>/dev/null || break
    sleep 0.25
done
[ "$ready" -eq 1 ] || { printf 'FAIL: html stub on port %s never became ready\n' "$tag_port"; exit 1; }
case_run "no schema block" 1 "returned no readable schema.version" \
    "$gate" --image "$THORNOTES_IMAGE" --driver mysql \
    --healthz http://127.0.0.1:$tag_port/healthz
kill "$STUB_PID" 2>/dev/null

printf '\n'
[ "$fail" -eq 0 ] && { echo "ALL CASES PASS"; exit 0; }
echo "FAILURES PRESENT"
exit 1
