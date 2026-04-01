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

### CodeMirror 6 editor (mobile editor experience)
**What:** EasyMDE (built on CodeMirror 5) has a poor touch experience. CodeMirror 6 is a complete rewrite with first-class mobile support and a modular architecture.

**Why:** The PWA shell and responsive layout shipped in v0.9.0.0. The remaining mobile gap is the editor itself — toolbar overflow, virtual keyboard handling, and cursor interaction are all suboptimal on touch devices.

**How:** Replace EasyMDE with a CodeMirror 6 setup: `@codemirror/view`, `@codemirror/lang-markdown`, `@codemirror/commands`. Requires reimplementing the preview toggle and toolbar. Significant effort — evaluate bundling approach (esbuild/rollup) vs. serving modules directly.

**Where:** `web/static/js/app.js`, `web/static/js/vendor/`, `web/templates/app.html`

---

---


## Completed

### Startup reconciliation progress + --skip-reconciliation flag
**Completed:** v0.12.0.0 — Startup reconciliation now wired up in `main.go` (was implemented but never called). `Reconcile` logs starting/progress/complete with note counts. `--skip-reconciliation` / `THORNOTES_SKIP_RECONCILIATION` bypasses the scan on trusted restarts.

### Timezone-aware "today" for journal entries
**Completed:** v0.10.0.0 — `GET /api/v1/journals/{id}/today?tz=America/New_York`. Handler validates via `time.LoadLocation`, falls back to UTC if omitted. Frontend passes `Intl.DateTimeFormat().resolvedOptions().timeZone`. Invalid tz → HTTP 400.

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

### Disk-full error → 507 response + persistent UI banner
**Completed:** v0.11.0.0 — `isENOSPC` detects `syscall.ENOSPC` in `fs.go`; wraps as `apperror.DiskFull()` (HTTP 507). `autoSave` in `app.js` detects `e.status === 507` and shows a persistent red banner: "Your disk is full — note could not be saved."

### Lazy-loading note list: GET /api/v1/folders/{id}/notes
**Completed:** v0.1.0.0 (2026-03-29) — Endpoint registered in router, `loadedFolderIds` tracking in `app.js`, folder expand fetches notes lazily.

### Mobile / PWA
**Completed:** v0.9.0.0 — PWA manifest + service worker (SW at `/sw.js` for root scope); SVG icons 192×192 and 512×512; responsive off-canvas sidebar with hamburger toggle and overlay; touch-friendly tap targets (≥ 32–44 px); safe-area insets; `100dvh`; bottom-sheet modals on mobile; auto-close sidebar on note open. CodeMirror 6 editor replacement deferred (significant effort, separate TODO).

### MySQL/PostgreSQL support
**Completed:** v0.7.0.0 — `internal/repository/mysql/` implements all repository interfaces against `database/sql`. MySQL migrations in `internal/db/mysql_migrations/`. FULLTEXT search via `MATCH...AGAINST` in boolean mode. Select via `THORNOTES_DB_DRIVER=mysql` + individual `THORNOTES_DB_HOST/NAME/USER/PASSWORD` variables (DSN assembled internally). Docker Compose with MariaDB example in README (updated v0.12.2.0).
