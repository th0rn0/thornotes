#!/bin/sh
# deploy-lag-check.test.sh — scripted check of scripts/deploy-lag-check.sh.
#
# Every decision the checker can make is exercised against a real git fixture
# (a throwaway repo with an origin bare remote, two merge commits on main) and
# a stub /healthz that reports a chosen build.version. Production is never
# touched: the served side is always the stub, the stale states are simulated
# by pointing build.version at older fixture commits — the same shape the 653
# incident had (production on an ancestor of main).
#
# Usage:
#   scripts/deploy-lag-check.test.sh
#
# Exit 0 when every case matches, 1 on the first mismatch (diff printed).

set -u

here=$(cd "$(dirname "$0")" && pwd)
check="$here/deploy-lag-check.sh"
[ -x "$check" ] || { echo "FAIL: $check missing or not executable"; exit 1; }

PY=${PY:-python3}
command -v git >/dev/null 2>&1 || { echo "FAIL: git not available"; exit 1; }
command -v "$PY" >/dev/null 2>&1 || { echo "FAIL: $PY not available (stub healthz dependency)"; exit 1; }

fail=0
note() { printf '\n== %s\n' "$*"; }
ok() { printf 'PASS: %s\n' "$*"; }

# ---- fixture repo: main with two merge commits (two "builds" ahead each) ----
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
ORIGIN="$tmp/origin.git"
FIXTURE="$tmp/fixture"
git init --bare --quiet "$ORIGIN"
git init --quiet -b main "$FIXTURE"
git -C "$FIXTURE" config user.email test@example.com
git -C "$FIXTURE" config user.name fixture
blob1="$FIXTURE/file"
echo one > "$blob1"
git -C "$FIXTURE" add -A && git -C "$FIXTURE" commit --quiet -m "initial"
base=$(git -C "$FIXTURE" rev-parse HEAD)
git -C "$FIXTURE" checkout --quiet -b pr1
echo two > "$blob1"
git -C "$FIXTURE" commit --quiet -am "pr1 work"
git -C "$FIXTURE" checkout --quiet main
# squash-merge, mirroring how this repo merges PRs: one commit per build
git -C "$FIXTURE" merge --quiet --squash pr1
git -C "$FIXTURE" commit --quiet -m "pr1 work (#1)"
merge1=$(git -C "$FIXTURE" rev-parse HEAD)
git -C "$FIXTURE" checkout --quiet -b pr2
echo three > "$blob1"
git -C "$FIXTURE" commit --quiet -am "pr2 work"
git -C "$FIXTURE" checkout --quiet main
git -C "$FIXTURE" merge --quiet --squash pr2
git -C "$FIXTURE" commit --quiet -m "pr2 work (#2)"
head_sha=$(git -C "$FIXTURE" rev-parse HEAD)
git -C "$FIXTURE" checkout --quiet -b unmerged
echo stray > "$blob1"
git -C "$FIXTURE" commit --quiet -am "stray work"
stray=$(git -C "$FIXTURE" rev-parse HEAD)
git -C "$FIXTURE" checkout --quiet main
git -C "$FIXTURE" remote add origin "$ORIGIN"
git -C "$FIXTURE" push --quiet origin main
# provenance of the fixture numbers
printf 'fixture: base=%s merge1=%s head=%s stray(unmerged)=%s\n' "$base" "$merge1" "$head_sha" "$stray" >&2

# ---- stub healthz ------------------------------------------------------------
STUB_PIDS=""
stub_healthz() {
    port=$1; version=$2
    "$PY" -c '
import http.server, json, sys
port = int(sys.argv[1]); version = sys.argv[2]
body = json.dumps({"build": {"version": version},
        "checks": {"db_read": "ok", "db_write": "ok", "notes_root": "ok"},
        "schema": {"max_supported": 7, "version": 7},
        "startup": {"phase": "ready"}, "status": "ok"}).encode()
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
' "$port" "$version" >/dev/null 2>&1 &
    STUB_PIDS="$STUB_PIDS $!"
    ready=0
    for _ in $(seq 1 40); do
        if curl -fsS --max-time 1 "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then ready=1; break; fi
        kill -0 "$!" 2>/dev/null || break
        sleep 0.25
    done
    if [ "$ready" -ne 1 ]; then
        printf 'FAIL: stub healthz on port %s never became ready\n' "$port"
        exit 1
    fi
}

stop_stubs() {
    for p in $STUB_PIDS; do kill "$p" 2>/dev/null; done
    STUB_PIDS=""
}
trap 'stop_stubs; rm -rf "$tmp"' EXIT INT TERM

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
    printf '  output: %s\n' "$out"
}

STATE="$tmp/state"
URL() { printf 'http://127.0.0.1:%s/healthz' "$1"; }

note "1. usage error — missing --healthz exits 2"
case_run "missing --healthz" 2 "is required" "$check" --repo "$FIXTURE"

note "2. CANNOT-EVALUATE — unreachable /healthz exits 75, prints no alert"
case_run "unreachable healthz" 75 "CANNOT-EVALUATE" \
    "$check" --healthz "$(URL 18099)" --repo "$FIXTURE" --state "$STATE"

note "3. CANNOT-EVALUATE — body without a build block exits 75"
"$PY" -c '
import http.server, sys
port = int(sys.argv[1])
body = b"<html>proxy error page</html>"
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/html")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
' 18095 >/dev/null 2>&1 &
STUB_PIDS="$STUB_PIDS $!"
for _ in $(seq 1 40); do curl -fsS --max-time 1 "http://127.0.0.1:18095/healthz" >/dev/null 2>&1 && break; sleep 0.25; done
case_run "no build block" 75 "carries no build block" \
    "$check" --healthz "$(URL 18095)" --repo "$FIXTURE" --state "$STATE"
stop_stubs

note "4. healthy — deployed == origin/main: silent exit 0, state cleared"
stub_healthz 18081 "$head_sha"
case_run "deployed equals main" 0 "" \
    "$check" --healthz "$(URL 18081)" --repo "$FIXTURE" --state "$STATE"
[ ! -f "$STATE/behind" ] && [ ! -f "$STATE/fired" ] && ok "state cleared on healthy" \
    || { printf 'FAIL: state not cleared\n'; fail=1; }
stop_stubs

note "5. one build behind, within the lag budget — silent exit 0 (in-flight deploy)"
stub_healthz 18082 "$merge1"
case_run "one build behind, fresh" 0 "" \
    "$check" --healthz "$(URL 18082)" --repo "$FIXTURE" --state "$STATE" --lag-minutes 15
[ -f "$STATE/behind" ] && ok "behind-state recorded" \
    || { printf 'FAIL: behind-state not recorded\n'; fail=1; }
stop_stubs

note "6. one build behind, lag outlived the budget — ALERT (the 653 signature)"
stub_healthz 18088 "$merge1"
case_run "one build behind, aged" 1 "production has been BEHIND main" \
    "$check" --healthz "$(URL 18088)" --repo "$FIXTURE" --state "$STATE" --lag-minutes 0
case_run "one build behind, already fired — silent exit 75" 75 "" \
    "$check" --healthz "$(URL 18088)" --repo "$FIXTURE" --state "$STATE" --lag-minutes 0
stop_stubs

note "7. healthy again clears the fired state"
stub_healthz 18083 "$head_sha"
case_run "recovery" 0 "" "$check" --healthz "$(URL 18083)" --repo "$FIXTURE" --state "$STATE"
[ ! -f "$STATE/behind" ] && [ ! -f "$STATE/fired" ] && ok "state cleared after recovery" \
    || { printf 'FAIL: state not cleared after recovery\n'; fail=1; }
stop_stubs

note "8. two builds behind — ALERT immediately (more than --max-behind-builds)"
stub_healthz 18084 "$base"
case_run "two builds behind" 1 "BEHIND main by more than 1 build(s)" \
    "$check" --healthz "$(URL 18084)" --repo "$FIXTURE" --state "$STATE"
stop_stubs

note "9. deployed sha NOT an ancestor of main — ALERT immediately (backwards or foreign image)"
stub_healthz 18085 "$stray"
case_run "non-ancestor deployed sha" 1 "NOT an ancestor of origin/main" \
    "$check" --healthz "$(URL 18085)" --repo "$FIXTURE" --state "$STATE"
stop_stubs

note "10. build.version is not a sha — CANNOT-EVALUATE, never a false verdict"
stub_healthz 18086 "1.5.14"
case_run "non-sha version" 75 "is not a commit sha" \
    "$check" --healthz "$(URL 18086)" --repo "$FIXTURE" --state "$STATE"
stop_stubs

note "11. repo is not a git checkout — CANNOT-EVALUATE"
stub_healthz 18087 "$head_sha"
case_run "no repo" 75 "not a git checkout" \
    "$check" --healthz "$(URL 18087)" --repo "$tmp/nowhere" --state "$STATE"
stop_stubs

note "12. FORCE_BUILD_VERSION drill — same decision path, no /healthz at all"
FORCE_BUILD_VERSION="$base"
export FORCE_BUILD_VERSION
case_run "forced stale version" 1 "BEHIND main" \
    "$check" --repo "$FIXTURE" --state "$STATE" --lag-minutes 0
unset FORCE_BUILD_VERSION
stop_stubs

note "13. schema.version does not masquerade as build.version (stub 4 never fired an alert on it)"
true

printf '\n'
[ "$fail" -eq 0 ] && { echo "ALL CASES PASS"; exit 0; }
echo "FAILURES PRESENT"
exit 1
