# thornotes — TODOS

## V2 (deferred, design in V1 for easy addition)

### Docker integration smoke test
**What:** A CI job that pulls the freshly-pushed image and runs a basic health check
(`docker run ... /thornotes --help` or a live HTTP check).

**Why:** Currently CI verifies the build succeeds but not that the image actually
starts correctly. A broken entrypoint or missing embed would only surface at deploy time.

**How:** Add a `smoke-test` job after `build-push` that pulls
`th0rn0/thornotes:latest` and runs a quick sanity check.

**Where:** `.github/workflows/ci.yml`

---

### golangci-lint config (.golangci.yml)
**What:** Explicit lint rule config so rules are reproducible locally and in CI.

**Why:** Currently using golangci-lint defaults. Explicit config allows enabling
`errcheck`, `govet`, `staticcheck` consistently and avoiding surprise failures
when the linter adds new default rules.

**Where:** `.golangci.yml` at repo root

---

### Multi-arch Docker builds (amd64 + arm64)
**What:** Build and push `linux/arm64` alongside `linux/amd64` in CI.

**Why:** Self-hosters on Raspberry Pi / NAS (arm64) can pull natively without
emulation. Particularly relevant for thornotes' self-hosted positioning.

**How:** Add `platforms: linux/amd64,linux/arm64` to `docker/build-push-action`.
Requires QEMU setup via `docker/setup-qemu-action@v3`. Build time increases ~3 min.

**Where:** `.github/workflows/ci.yml` (build-push job)

---

### Startup reconciliation progress + --skip-reconciliation flag
**What:** The startup reconciliation scan (comparing SHA-256 of every .md file vs
`content_hash` in DB) can take minutes for large note corpora. Server appears
unresponsive with no log output.

**Why:** Self-hosters will see a mysterious 2-4 minute black hole on restart. With
5000 notes at ~50ms per file hash → ~4 minutes of silence.

**How:** Log progress every 100 notes: `slog.Info("reconciling", "progress", "1234/5000")`.
Add `--skip-reconciliation` flag to bypass scan on trusted restarts.

**Where:** startup reconciliation code in `internal/notes/service.go` or `cmd/thornotes/main.go`

---

### Disk-full error → clear user notification (507 response)
**What:** When `FileStore.Write()` fails because disk is full, the auto-save silently
fails and the user loses their last 2 seconds of edits with no notification.

**Why:** Prevents silent data loss. Users on self-hosted deployments with small disks
will hit this.

**How:** Detect `syscall.ENOSPC` in `fs.go`. Map to `ErrDiskFull` sentinel. In the
PATCH note handler, map `ErrDiskFull` → HTTP 507. In `app.js`, display a persistent
error banner: "Your disk is full — note could not be saved."

**Where:** `internal/notes/fs.go`, `internal/handler/notes.go`, `web/static/js/app.js`

---

### Git-backed version history
**What:** Every save is a `git commit` on disk. Full version history, diffing,
branching (draft/published). UI shows a timeline of every note's history.

**Why:** Subagent identified this as the "whoa" feature. No other self-hosted note
app does this well.

**How:** Use `go-git` (pure Go git implementation) or shell out to `git`. Toggle with
`--enable-git-history` flag.

**Where:** `internal/notes/fs.go` (wrap Write/FolderRename with git commit)

---

### LLM context endpoint
**What:** `GET /api/v1/notes/context?folder_id=X` returns concatenated note content
ready to paste into an LLM prompt.

**Response:** `{ context: string, note_count: int, truncated: bool, char_limit: int }`
Max 200,000 chars (~50k tokens). Truncates oldest notes first.

**Why:** File-first + LLM context is the product thesis for the hosted-service path.

---

### zerolog logging & gin web server
**What:** Replace `log/slog` with `zerolog` for structured logging, and replace the stdlib `net/http` mux with `gin`.

**Why:** zerolog is significantly faster for high-throughput structured logging (zero-alloc JSON). gin adds request-level middleware (request ID, panic recovery, access logging) and cleaner route grouping without boilerplate.

**How:** Add `github.com/rs/zerolog` and `github.com/gin-gonic/gin`. Swap handler signatures from `(w http.ResponseWriter, r *http.Request)` to `*gin.Context`. Move route definitions from `internal/router/router.go` into gin route groups. Replace all `slog.Info/Error/Warn` call sites.

**Where:** `internal/router/router.go`, `internal/handler/*.go`, `cmd/thornotes/main.go`

---

### MySQL/PostgreSQL support
**What:** Implement the `SearchRepository` MySQL FULLTEXT variant and the core repository
interfaces against MySQL/PostgreSQL.

**Why:** Hosted-service deployment needs a shared DB, not SQLite.

**How:** Create `internal/repository/mysql/` when implementing. The interfaces in
`internal/repository/interfaces.go` are already the abstraction point.

---

### Mobile / PWA
**What:** EasyMDE is not good on mobile. Evaluate CodeMirror 6 as a replacement.
Add viewport meta, touch-friendly UI adjustments.

**Why:** Research shows mobile-responsive web UI is now expected for self-hosted apps.

---

### Timezone-aware "today" for journal entries
**What:** Pass the user's browser timezone to `GET /api/v1/journals/{id}/today` so the server computes the correct local date.

**Why:** Currently uses server UTC. A user in UTC+9 or UTC-8 may get the wrong date for several hours of their day. Cosmetic but affects daily journaling correctness for non-UTC users.

**How:** Frontend adds `?tz=America/New_York` (via `Intl.DateTimeFormat().resolvedOptions().timeZone`). Handler uses `time.LoadLocation(tz)` to compute today's date in that zone. Validate the timezone string to prevent injection.

**Where:** `internal/handler/journals.go`, `web/static/js/app.js`

**Depends on / blocked by:** Daily journal feature (v0.5.0.0) must ship first.

---

---

### Security: hash API tokens before DB storage
**What:** API tokens are stored in plaintext in the `api_tokens` table. If the DB leaks, all tokens are immediately usable.

**Why:** CSO audit finding #1 (HIGH, confidence 9/10). Password-equivalent secrets should be stored as SHA-256(token). GitHub and Linear use this pattern for PATs.

**How:** `CreateToken` stores `SHA-256(raw_token)`, returns raw token once to client. `GetByToken` hashes the incoming token before querying `WHERE token_hash = ?`. Session tokens have the same pattern.

**Where:** `internal/repository/sqlite/api_tokens.go:22`

---

### Security: SHA-pin GitHub Actions
**What:** Third-party GitHub Actions are pinned to mutable version tags (v3, v6) rather than immutable SHA hashes.

**Why:** CSO audit finding #2 (HIGH, confidence 9/10). A compromised action repo could push malicious code to the tag, stealing `DOCKERHUB_TOKEN` and pushing a backdoored image.

**How:** Pin every third-party action to its full commit SHA. Add Dependabot to keep pins updated.

**Where:** `.github/workflows/ci.yml:105`

---

### Security: THORNOTES_SECURE_COOKIES env var
**What:** Session cookie `Secure` flag is hardcoded `false`. Without HTTPS, the cookie is sent in cleartext.

**Why:** CSO audit finding #3 (MEDIUM, confidence 8/10). Enables session hijacking via passive network capture on HTTP deployments.

**How:** Add `THORNOTES_SECURE_COOKIES=true` env var (default true). Document in Dockerfile that it can be set to false for local HTTP-only deployments.

**Where:** `internal/handler/auth.go:73`, `internal/config/config.go`

---

## Completed

### Disk watcher + SSE live sync
**Completed:** v0.3.0.0 (2026-03-30) — Polling watcher (`internal/notes/watcher.go`) checks all notes on disk every `THORNOTES_WATCH_INTERVAL` (default 30s). Changes update the DB and push `notes_changed` SSE events to connected browser tabs via `GET /api/v1/events`. Frontend auto-refreshes the tree and reloads the open note (if unsaved edits are not in progress).



### Rate limiter: X-Forwarded-For + --trusted-proxy flag
**Completed:** v0.1.0.0 (2026-03-29) — Implemented in `internal/security/ratelimit.go` with `--trusted-proxy` CIDR flag in `internal/config/config.go`.

### Lazy-loading note list: GET /api/v1/folders/{id}/notes
**Completed:** v0.1.0.0 (2026-03-29) — Endpoint registered in router, `loadedFolderIds` tracking in `app.js`, folder expand fetches notes lazily.
