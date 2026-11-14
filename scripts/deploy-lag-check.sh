#!/bin/sh
# deploy-lag-check — report production serving a commit that main has replaced.
#
# The failure it exists for (ticket 653, 2026-11-14): two main builds ran at
# once, both push the mutable tag th0rn0/thornotes:latest, the older build's
# push finished second, the tag moved BACKWARDS, and watchtower redeployed the
# previous merge to production. Production was correct, then ran BEHIND main
# for 24 minutes — a poller saw aa6edb30 -> 239bbfe1 in real time — with every
# CI dashboard green. CI status cannot answer "is it deployed"; only the served
# /healthz compared against main can.
#
# What it decides, from the SERVED endpoint (not the container):
#
#   deployed == origin/main head              → healthy. Silent, exit 0.
#   deployed NOT an ancestor of origin/main   → ALERT. Production runs code
#                                               main does not contain: a
#                                               backwards tag move to a
#                                               de-ancestored sha, or a foreign
#                                               image. Never wait this out.
#   main ahead by more than --max-behind-builds builds → ALERT immediately.
#   main ahead by <= that many builds          → record when the behind-state
#                                               was first seen; ALERT once it
#                                               outlives --lag-minutes. A
#                                               normal deploy closes the gap
#                                               within a watchtower interval,
#                                               so a lag that persists is the
#                                               653 signature, not an
#                                               in-flight deploy.
#
# A build means a push to main. This repo squash-merges, so one commit on main
# is one build, and "builds ahead" is the commit count of
# deployed..origin/main (verified against #393/#395: main at aa6edb3 with
# production on 239bbfe was one build / one commit ahead). If main ever gains
# true merge commits, a merge of a multi-commit PR would read as several
# builds; the lag rule still catches the state, just a run earlier.
#
# Absent evidence is not permission: an unreachable /healthz, an unparseable
# build.version, or a failed git fetch exit 75 (cannot evaluate) and print
# nothing alert-shaped. The caller decides how misses escalate.
#
# Usage:
#   scripts/deploy-lag-check.sh \
#     --healthz https://notes.example.com/healthz \
#     --repo /path/to/thornotes-checkout
#
#   --healthz             URL of the SERVED /healthz. Required.
#   --repo                checkout of thornotes with `origin` = the GitHub
#                         remote; the script fetches origin/main into it.
#                         Required.
#   --state               directory for the behind-state ledger. Required for
#                         persistent deployment (the lag rule needs the first-
#                         seen timestamp across runs); omit for one-shot
#                         drills, which then evaluate the immediate rules only.
#   --lag-minutes         a behind-state older than this alerts (default 15).
#   --max-behind-builds   main ahead by more than this many builds alerts at
#                         once (default 1: two builds ahead is already past
#                         any in-flight window).
#   --curl-timeout        seconds to wait for /healthz (default 10).
#
# Env overrides (same values as the flags, for the cron wrapper):
#   DEPLOY_LAG_HEALTHZ, DEPLOY_LAG_REPO, DEPLOY_LAG_STATE,
#   DEPLOY_LAG_LAG_MINUTES, DEPLOY_LAG_MAX_BEHIND_BUILDS, DEPLOY_LAG_CURL_TIMEOUT
#   FORCE_BUILD_VERSION   pretend /healthz reported this build.version — drill
#                         the decision path against a genuinely stale state
#                         without touching production.
#
# Exit codes: 0 = healthy or within the lag budget (silent), 1 = alert
#             condition (message printed, one line per condition),
#             2 = usage error, 75 = cannot evaluate.
set -u

prog=$(basename "$0")

usage() {
    sed -n '2,58p' "$0" | sed 's/^# \{0,1\}//' >&2
    exit 2
}

die() { printf '%s: %s\n' "$prog" "$1" >&2; exit 2; }

HEALTHZ_URL=${DEPLOY_LAG_HEALTHZ:-}
REPO=${DEPLOY_LAG_REPO:-}
STATE_DIR=${DEPLOY_LAG_STATE:-}
LAG_MINUTES=${DEPLOY_LAG_LAG_MINUTES:-15}
MAX_BEHIND_BUILDS=${DEPLOY_LAG_MAX_BEHIND_BUILDS:-1}
CURL_TIMEOUT=${DEPLOY_LAG_CURL_TIMEOUT:-10}
FORCE_BUILD_VERSION=${FORCE_BUILD_VERSION:-}

while [ $# -gt 0 ]; do
    case "$1" in
        --healthz)             HEALTHZ_URL=${2-}; [ $# -ge 2 ] || die "--healthz needs a value"; shift ;;
        --repo)                REPO=${2-}; [ $# -ge 2 ] || die "--repo needs a value"; shift ;;
        --state)               STATE_DIR=${2-}; [ $# -ge 2 ] || die "--state needs a value"; shift ;;
        --lag-minutes)         LAG_MINUTES=${2-}; [ $# -ge 2 ] || die "--lag-minutes needs a value"; shift ;;
        --max-behind-builds)   MAX_BEHIND_BUILDS=${2-}; [ $# -ge 2 ] || die "--max-behind-builds needs a value"; shift ;;
        --curl-timeout)        CURL_TIMEOUT=${2-}; [ $# -ge 2 ] || die "--curl-timeout needs a value"; shift ;;
        -h|--help) usage ;;
        *) die "unknown argument: $1 (see --help)" ;;
    esac
    shift
done

[ -n "$HEALTHZ_URL" ] || [ -n "$FORCE_BUILD_VERSION" ] || die "--healthz is required (or set DEPLOY_LAG_HEALTHZ; FORCE_BUILD_VERSION drills may omit it)"
[ -n "$REPO" ] || die "--repo is required (or set DEPLOY_LAG_REPO)"
case "$MAX_BEHIND_BUILDS" in ''|*[!0-9]*) die "--max-behind-builds must be a number, got: $MAX_BEHIND_BUILDS" ;; esac
case "$LAG_MINUTES" in ''|*[!0-9]*) die "--lag-minutes must be a number, got: $LAG_MINUTES" ;; esac

cannot() { printf 'CANNOT-EVALUATE: %s\n' "$1" >&2; exit 75; }
alert()  { printf 'ALERT: %s\n' "$1"; }
log() { # keep a run ledger next to the state, when state is used
    [ -n "$STATE_DIR" ] || return 0
    mkdir -p "$STATE_DIR" || return 0
    printf '%s %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$1" >> "$STATE_DIR/log"
}

# ---- 1. The served build.version --------------------------------------------

if [ -n "$FORCE_BUILD_VERSION" ]; then
    deployed=$FORCE_BUILD_VERSION
else
    body=$(curl -fsS --max-time "$CURL_TIMEOUT" "$HEALTHZ_URL" 2>/dev/null)
    if [ -z "$body" ]; then
        cannot "served /healthz at $HEALTHZ_URL is unreachable or returned nothing — a deploy can be silently backwards while the endpoint is down, so this must be watched from the service probe instead"
    fi
    # Target the build block only: the body also carries schema.version, and
    # schema.version is a migration counter, not a commit. Whitespace is
    # stripped first so both compact and pretty-printed bodies match.
    body=$(printf '%s' "$body" | tr -d ' \t\n')
    build_block=$(printf '%s' "$body" | grep -o '"build":{[^}]*}')
    if [ -z "$build_block" ]; then
        cannot "/healthz at $HEALTHZ_URL answered but carries no build block — the body did not parse as this build's health report"
    fi
    deployed=$(printf '%s' "$build_block" | sed -n 's/.*"version":[[:space:]]*"\([^"]*\)".*/\1/p')
    if [ -z "$deployed" ]; then
        cannot "/healthz at $HEALTHZ_URL answered but build.version is unreadable — refusing to read a deploy verdict from a body without it"
    fi
fi
case "$deployed" in
    [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;;
    *) cannot "build.version '$deployed' is not a commit sha (expected hex) — refusing to compare it with main" ;;
esac

# ---- 2. origin/main's head ----------------------------------------------------

if ! command -v git >/dev/null 2>&1; then
    cannot "git is not available, so origin/main's head cannot be resolved"
fi
# .git may be a directory (a normal checkout) or a file (a worktree).
[ -e "$REPO/.git" ] || cannot "--repo $REPO is not a git checkout"
if ! fetch_err=$(cd "$REPO" && git fetch --quiet origin main 2>&1); then
    cannot "git fetch origin main failed in $REPO: $(printf '%s' "$fetch_err" | tail -1)"
fi
main=$(git -C "$REPO" rev-parse --verify --quiet origin/main^{commit}) || \
    cannot "cannot resolve origin/main in $REPO"

# ---- 3. Decide ----------------------------------------------------------------

if [ "$deployed" = "$main" ]; then
    log "healthy: deployed $deployed equals origin/main"
    if [ -n "$STATE_DIR" ]; then
        rm -f "$STATE_DIR/behind" "$STATE_DIR/fired"
    fi
    exit 0
fi

if ! git -C "$REPO" merge-base --is-ancestor -- "$deployed" "$main" 2>/dev/null; then
    log "alert: deployed $deployed is NOT an ancestor of origin/main $main"
    alert "deployed sha $deployed is NOT an ancestor of origin/main $main — production runs code main does not contain (a backwards tag move or a foreign image). Do not wait for watchtower: pin :latest to main's sha-<commit> tag and redeploy. History: ticket 653."
    exit 1
fi

commits_ahead=$(git -C "$REPO" rev-list --count "$deployed..$main")

behind_note="deployed $deployed, origin/main $main ($commits_ahead commit(s) = build(s) ahead)"

if [ "$commits_ahead" -gt "$MAX_BEHIND_BUILDS" ]; then
    log "alert: immediate — $behind_note"
    alert "production is BEHIND main by more than $MAX_BEHIND_BUILDS build(s): $behind_note. A normal watchtower deploy closes the gap in minutes; more than $MAX_BEHIND_BUILDS means a deploy was lost or reversed (see ticket 653). Check Drone build history for main's head and restart the newest build if :latest does not match it."
    exit 1
fi

# Within the immediate budget: the lag only becomes an alert once it has
# outlived --lag-minutes, measured from when THIS deployed sha was first seen
# behind main.
if [ -z "$STATE_DIR" ]; then
    log "behind within budget (no state dir, cannot age the lag): $behind_note"
    exit 0
fi
mkdir -p "$STATE_DIR" || cannot "cannot create state dir $STATE_DIR"
behind_file="$STATE_DIR/behind"
now=$(date +%s)
if [ -f "$behind_file" ]; then
    first_seen=$(cut -d' ' -f1 "$behind_file" 2>/dev/null)
    seen_sha=$(cut -d' ' -f2 "$behind_file" 2>/dev/null)
fi
if [ -z "${first_seen:-}" ] || [ "${seen_sha:-}" != "$deployed" ]; then
    printf '%s %s\n' "$now" "$deployed" > "$behind_file"
    first_seen=$now
    log "behind first seen: $behind_note"
fi
age_min=$(( (now - first_seen) / 60 ))
if [ "$age_min" -lt "$LAG_MINUTES" ]; then
    log "behind within budget (age ${age_min}m < ${LAG_MINUTES}m): $behind_note"
    exit 0
fi

if [ -f "$STATE_DIR/fired" ]; then
    # Already alerted for this behind-state; stay silent rather than re-fire
    # every run. The caller re-arms by clearing the state when healthy.
    log "behind persists (age ${age_min}m) but alert already fired for $deployed; staying silent"
    exit 75
fi
: > "$STATE_DIR/fired"
log "alert: lag aged ${age_min}m >= ${LAG_MINUTES}m — $behind_note"
alert "production has been BEHIND main for ~${age_min} minutes: $behind_note. This is the ticket-653 signature: :latest moved backwards (or a deploy stalled) while CI stayed green. Check Drone build history for main's head, compare with the sha-<commit> tag in the registry, and restart the newest main build if :latest does not match it."
exit 1
