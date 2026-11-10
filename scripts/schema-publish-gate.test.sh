#!/bin/sh
# schema-publish-gate.test.sh — scripted check of scripts/schema-publish-gate.sh.
#
# The publish gate only protects production while it actually gates: if a
# refactor ever removes the comparison, the gate would pass every build
# quietly. So this harness runs two tables:
#
#   1. the gate's decision table against a stub /healthz (python3
#      http.server; no docker, no production access) — every case must match;
#   2. a NEGATIVE CONTROL: a copy of the gate with the comparison block
#      removed (the "decide: begin"/"decide: end" markers) must make table
#      (1) FAIL. A table that passes a gate that never refuses is decoration;
#      this harness fails the build instead.
#
# Run in CI (.drone.yml step schema-gate-test) and locally:
#   sh scripts/schema-publish-gate.test.sh
#
# Env:
#   GATE  implementation under test (default scripts/schema-publish-gate.sh
#         next to this file; pass a path, not a command line)
#   PY    python3 binary for the stub server (default python3)
#
# Exit 0 when every case matches AND the negative control failed as required.

set -u

here=$(cd "$(dirname "$0")" && pwd)
GATE=${GATE:-"$here/schema-publish-gate.sh"}
PY=${PY:-python3}

# The candidate ceiling for every case. Kept at .drone.yml's
# THORNOTES_SCHEMA_MAX_MYSQL so the numbers read together; the stub reports
# versions on both sides of it, so no case depends on the number moving.
CANDIDATE_MAX=7
HOST=127.0.0.1

STUB_PID=""
RUN_GATE=""
TABLE_FAIL=0
fail=0

note() { printf '\n== %s\n' "$*"; }
ok() { printf 'PASS: %s\n' "$*"; }

command -v curl >/dev/null 2>&1 || { echo "FAIL: curl not available"; exit 1; }
command -v "$PY" >/dev/null 2>&1 || { echo "FAIL: $PY not available (stub healthz dependency)"; exit 1; }
[ -f "$GATE" ] || { echo "FAIL: gate under test missing: $GATE"; exit 1; }
# The gate is invoked unquoted on purpose (SC2086): RUN_GATE may be two words,
# "sh <path>", when the file is not executable.
if [ -x "$GATE" ]; then RUN_GATE="$GATE"; else RUN_GATE="sh $GATE"; fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT INT TERM

# start_stub <port> <body-file> — serve the file's bytes as /healthz
start_stub() {
    port=$1; bodyfile=$2
    "$PY" -c '
import http.server, sys
port = int(sys.argv[1]); body = open(sys.argv[2], "rb").read()
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
' "$port" "$bodyfile" >/dev/null 2>&1 &
    STUB_PID=$!
    ready=0
    for i in $(seq 1 40); do
        if curl -fsS --max-time 1 "http://$HOST:$port/healthz" >/dev/null 2>&1; then ready=1; break; fi
        kill -0 "$STUB_PID" 2>/dev/null || break
        sleep 0.25
    done
    if [ "$ready" -ne 1 ]; then
        printf 'FAIL: stub healthz on port %s never became ready\n' "$port"
        exit 1
    fi
}

stop_stub() {
    if [ -n "$STUB_PID" ]; then
        kill "$STUB_PID" 2>/dev/null
        wait "$STUB_PID" 2>/dev/null
        STUB_PID=""
    fi
}

# case_run <name> <expected rc> <expected verdict fragment> <cmd...>
case_run() {
    name=$1; expect_rc=$2; expect_msg=$3
    shift 3
    out=$("$@" 2>&1)
    rc=$?
    if [ "$rc" -ne "$expect_rc" ]; then
        printf 'FAIL: %s — exit %s, expected %s\n  output: %s\n' "$name" "$rc" "$expect_rc" "$out"
        TABLE_FAIL=1
    elif [ -z "$expect_msg" ]; then
        ok "$name (exit $rc)"
    elif printf '%s' "$out" | grep -q "$expect_msg"; then
        ok "$name (exit $rc, verdict matched)"
    else
        printf 'FAIL: %s — expected verdict fragment "%s", got: %s\n' "$name" "$expect_msg" "$out"
        TABLE_FAIL=1
    fi
    printf '  gate output: %s\n' "$out"
}

# run_cases <gate-cmd> <base-port> — the decision table, against any gate.
run_cases() {
    gate=$1; base=$2

    printf '%s' '{"schema":{"max_supported":7,"version":6},"status":"ok"}'       > "$tmpdir/behind.json"
    printf '%s' '{"schema":{"max_supported":7,"version":8},"status":"ok"}'       > "$tmpdir/ahead.json"
    printf '%s' '{"schema":{"max_supported":7,"version":7},"status":"ok"}'       > "$tmpdir/equal.json"
    printf '%s' '{"schema":{"version":7},"status":"ok"}'                         > "$tmpdir/no-max.json"
    printf '%s' '{"schema":{"max_supported":7,"version":"seven"},"status":"ok"}' > "$tmpdir/nonnumeric.json"
    printf '%s' '<html><body>proxy error page</body></html>'                     > "$tmpdir/noschema.html"
      # The real body, in shape: the build stamp comes BEFORE the schema object and its
      # "version" is a quoted commit SHA. A gate that takes the first "version" it sees
      # reads the SHA, fails to parse an integer and refuses every publish -- which is what
      # the first version of this gate did, because every other fixture here puts "schema"
      # first and carries no build block. The table was green; production would have
      # refused the happy path.
      printf '%s' '{"build":{"version":"0671e62234f192590d3f565cc5bd3dd91bdf9947"},"checks":{"db_read":"ok","db_write":"ok","notes_root":"ok"},"schema":{"max_supported":7,"version":7},"status":"ok"}' > "$tmpdir/prod-shaped.json"

    port=$((base + 1))
    start_stub "$port" "$tmpdir/behind.json"
    case_run "live behind candidate" 0 \
        "PROCEED -- candidate MySQL ceiling $CANDIDATE_MAX vs live database version 6" \
        $gate --url "http://$HOST:$port/healthz" --engine mysql --max "$CANDIDATE_MAX"
    stop_stub

    port=$((base + 2))
    start_stub "$port" "$tmpdir/ahead.json"
    case_run "live ahead of candidate" 1 \
        "REFUSE -- candidate MySQL ceiling $CANDIDATE_MAX vs live database version 8" \
        $gate --url "http://$HOST:$port/healthz" --engine mysql --max "$CANDIDATE_MAX"
    stop_stub

    port=$((base + 3))
    start_stub "$port" "$tmpdir/equal.json"
    case_run "live equal to candidate" 0 \
        "PROCEED -- candidate MySQL ceiling $CANDIDATE_MAX vs live database version 7" \
        $gate --url "http://$HOST:$port/healthz" --engine mysql --max "$CANDIDATE_MAX"
    stop_stub

    # Nothing listens on this port: the unreachable case, for real.
    case_run "healthz unreachable" 1 \
        "unreachable or returned nothing" \
        $gate --url "http://$HOST:$((base + 9))/healthz" --engine mysql --max "$CANDIDATE_MAX"

      port=$((base + 4))
      start_stub "$port" "$tmpdir/prod-shaped.json"
      case_run "real /healthz shape (build stamp before schema)" 0 \
          "PROCEED -- candidate MySQL ceiling $CANDIDATE_MAX vs live database version 7" \
          $gate --url "http://$HOST:$port/healthz" --engine mysql --max "$CANDIDATE_MAX"
      stop_stub

      port=$((base + 5))
    start_stub "$port" "$tmpdir/noschema.html"
    case_run "healthz without schema block" 1 \
        "no readable schema.version" \
        $gate --url "http://$HOST:$port/healthz" --engine mysql --max "$CANDIDATE_MAX"
    stop_stub

      port=$((base + 6))
    start_stub "$port" "$tmpdir/nonnumeric.json"
    case_run "non-numeric version" 1 \
        "no readable schema.version" \
        $gate --url "http://$HOST:$port/healthz" --engine mysql --max "$CANDIDATE_MAX"
    stop_stub

      port=$((base + 7))
    start_stub "$port" "$tmpdir/no-max.json"
    case_run "schema block without max_supported" 1 \
        "no readable schema.max_supported" \
        $gate --url "http://$HOST:$port/healthz" --engine mysql --max "$CANDIDATE_MAX"
    stop_stub
}

# ---- 1. The real gate must pass every case -----------------------------------

note "decision table against the gate under test: $GATE"
TABLE_FAIL=0
run_cases "$RUN_GATE" 18300
if [ "$TABLE_FAIL" -ne 0 ]; then
    echo "FAILURES PRESENT"
    exit 1
fi

# ---- 2. Negative control: strip the comparison, the table must fail ----------

note "negative control — the same table against a gate with the comparison removed"
mutant="$tmpdir/gate-without-comparison.sh"
awk '
    /^# ---- decide: begin/ { print ": # comparison removed by negative control"; skip = 1; next }
    /^# ---- decide: end/   { skip = 0; next }
    skip { next }
    { print }
' "$GATE" > "$mutant"

if ! sh -n "$mutant" 2>/dev/null; then
    echo "FAIL: removing the comparison produced a syntactically broken gate — fix the decide markers"
    fail=1
else
    TABLE_FAIL=0
    # The mutant is not executable; run_cases accepts a "sh <path>" command.
    run_cases "sh $mutant" 18400 > "$tmpdir/negative-control.log" 2>&1 || true
    nc_log=$(cat "$tmpdir/negative-control.log")
    if [ "$TABLE_FAIL" -eq 0 ]; then
        echo "FAIL: the test table PASSED against a gate with the comparison removed — it does not actually gate"
        printf '%s\n' "$nc_log"
        fail=1
    elif ! printf '%s' "$nc_log" | grep -q "FAIL: live ahead"; then
        echo "FAIL: the table failed against the comparison-less gate, but not on the 'live ahead of candidate' case — the control is not pinning the comparison"
        printf '%s\n' "$nc_log"
        fail=1
    else
        ok "table failed against the comparison-less gate (as required), caught by the 'live ahead of candidate' case"
        printf '%s\n' "$nc_log" | grep "^FAIL" || true
    fi
fi

printf '\n'
if [ "$fail" -eq 0 ]; then
    echo "ALL CASES PASS (including the negative control)"
    exit 0
fi
echo "FAILURES PRESENT"
exit 1
