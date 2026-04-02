# thornotes — TODOS

## V2 (deferred, design in V1 for easy addition)

### Git-backed version history
**What:** Every save is a `git commit` on disk. Full version history, diffing,
branching (draft/published). UI shows a timeline of every note's history.

**Why:** Subagent identified this as the "whoa" feature. No other self-hosted note
app does this well.

**How:** Use `go-git` (pure Go git implementation) or shell out to `git`. Toggle with
`--enable-git-history` flag.

**Where:** `internal/notes/fs.go` (wrap Write/FolderRename with git commit)

---

---


## Completed

### Deep linking
**Completed:** v0.17.0.0 — Note URLs reflect the folder path and note slug (`/My-Folder/my-note`). `history.pushState` on open, `popstate` for back/forward. Server-side `NoRoute` serves the app shell for all non-API/MCP paths so deep links survive a hard refresh. `document.title` updates to the note name.

### CodeMirror 6 editor
**Completed:** v0.16.0.0 — EasyMDE (CM5, 320KB) replaced with CM6 bundle (493KB, built with bun from `web/cm6-bundle/index.js`). Custom toolbar (bold, italic, H#, quote, lists, link, preview toggle, undo, redo). Preview pane using existing marked + hljs pipeline. Live dark-mode switching via CM6 `Compartment`. `make build-cm6` rebuilds the vendor file.

### LLM context endpoint
**Completed:** v0.15.0.0 — `GET /api/v1/notes/context?folder_id=X` returns concatenated note content ready to paste into an LLM prompt. Response: `{ context, note_count, truncated, char_limit }`. Max 200,000 chars, truncates oldest notes first. Both repos (SQLite + MySQL) implement `ListForContext`. Full integration test coverage (9 tests).

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

### golangci-lint config + multi-arch Docker builds + smoke test
**Completed:** v0.13.0.0 — `.golangci.yml` added with explicit linter set (`errcheck`, `govet`, `staticcheck`, `gosimple`, `ineffassign`, `unused`) using `default: none` for reproducibility. `build-push` CI job now builds `linux/amd64` and `linux/arm64` via QEMU + buildx. New `smoke-test` job pulls the freshly pushed image, starts a container, and verifies HTTP 200 before the `release` job runs. `setup-qemu-action@v3` SHA pinning deferred (TODO: pin via `gh api /repos/docker/setup-qemu-action/git/ref/tags/v3`).
