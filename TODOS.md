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


## Completed

### Security: hash API tokens before DB storage
**Completed:** v0.6.0.0 — Migration 004 adds `token_hash` + `prefix` columns. `Create` stores `SHA-256(raw)`, returns raw once. `GetByToken` hashes before lookup. Existing tokens invalidated by migration (users must regenerate).

### Security: SHA-pin GitHub Actions
**Completed:** v0.6.0.0 — All third-party actions in `ci.yml` pinned to immutable commit SHAs with version tag comment. `checkout@v4`, `setup-go@v5`, `golangci-lint-action@v6`, `setup-buildx@v3`, `login-action@v3`, `build-push@v6`, `discord-webhook@v6.0.0`.

### Security: THORNOTES_SECURE_COOKIES env var
**Completed:** v0.6.0.0 — `THORNOTES_SECURE_COOKIES` / `--secure-cookies` flag added (default `false`). Session cookie `Secure` field driven by config. Documented in README, Dockerfile, and Docker Compose example.

### Disk watcher + SSE live sync
**Completed:** v0.3.0.0 (2026-03-30) — Polling watcher (`internal/notes/watcher.go`) checks all notes on disk every `THORNOTES_WATCH_INTERVAL` (default 30s). Changes update the DB and push `notes_changed` SSE events to connected browser tabs via `GET /api/v1/events`. Frontend auto-refreshes the tree and reloads the open note (if unsaved edits are not in progress).



### Rate limiter: X-Forwarded-For + --trusted-proxy flag
**Completed:** v0.1.0.0 (2026-03-29) — Implemented in `internal/security/ratelimit.go` with `--trusted-proxy` CIDR flag in `internal/config/config.go`.

### Lazy-loading note list: GET /api/v1/folders/{id}/notes
**Completed:** v0.1.0.0 (2026-03-29) — Endpoint registered in router, `loadedFolderIds` tracking in `app.js`, folder expand fetches notes lazily.

### MySQL/PostgreSQL support
**Completed:** v0.7.0.0 — `internal/repository/mysql/` implements all repository interfaces against `database/sql`. MySQL migrations in `internal/db/mysql_migrations/`. FULLTEXT search via `MATCH...AGAINST` in boolean mode. Select via `THORNOTES_DB_DRIVER=mysql` + `THORNOTES_DB_DSN`. Docker Compose with MySQL example in README.
