# Changelog

All notable changes to thornotes are documented here.

## [0.14.0.0] - 2026-04-01

### Changed
- **MCP transport upgraded to Streamable HTTP (2025-03-26)** — the previous implementation used the older `2024-11-05` JSON-only POST transport. The new transport adds:
  - `GET /mcp` — server-sent event stream for server-initiated messages; thornotes holds it open with 25-second keepalive comments (no server-push messages yet).
  - `DELETE /mcp` — terminates a session.
  - **Session management** — `initialize` generates a cryptographically random session ID returned in the `Mcp-Session-Id` response header; clients include it in subsequent requests; unknown session IDs return `404`.
  - **Notifications return `202`** — any JSON-RPC message without an `id` field (notifications) returns `202 Accepted` with no body, matching the spec. Previously only `notifications/initialized` was handled this way.
  - **Batch requests** — POST body may be a JSON array of JSON-RPC messages; responses are collected and returned as a JSON array; all-notification batches return `202`.
  - Protocol version field updated from `2024-11-05` to `2025-03-26`.

## [0.13.6.0] - 2026-04-01

### Fixed
- **Copy button works in non-HTTPS contexts** — `navigator.clipboard` is unavailable on HTTP (e.g. `http://192.168.x.x:8080`); the `.catch(() => {})` silently swallowed the error so the "Token copied" notification fired but nothing reached the clipboard. Added a `copyToClipboard` helper with a `document.execCommand('copy')` fallback. Fixes the token copy button in the account modal and the share-link copy button.
- **Service worker cache bumped** — cache key updated to `thornotes-v0.13.6.0` so browsers with the stale cached `app.js` pick up the fix on next load.

## [0.13.5.0] - 2026-04-01

### Fixed
- **MariaDB virtual column syntax** — `parent_id_key` in the `folders` table was defined as `BIGINT NOT NULL AS (expr) VIRTUAL`. MySQL 8.0 accepts `NOT NULL` before `AS`, but MariaDB requires the constraint to come after the generated column clause. MariaDB also does not accept `NOT NULL` on a `VIRTUAL` column at all. The `NOT NULL` constraint was removed; it was semantically redundant since `COALESCE(parent_id, 0)` never returns NULL. Fixes migration failure (`Error 1064`) on MariaDB 11 introduced in v0.7.0.0.

## [0.13.4.0] - 2026-04-01

### Fixed
- **Dirty migration recovery now uses `Force(-1)` instead of `Force(0)`** — `Force(0)` caused golang-migrate to look for a non-existent version 0 down file, producing a "no migration found for version 0" error. `Force(-1)` is the correct way to clear version tracking entirely; `Up()` then re-runs all migrations from scratch, which is safe because all up migrations use `CREATE TABLE IF NOT EXISTS`.

### Added
- **MariaDB/MySQL integration tests** — three tests in `internal/db/mysql_test.go` run when `THORNOTES_TEST_MYSQL_DSN` is set: migration correctness (all 6 tables exist), idempotency (second `OpenMySQL` call is a no-op), and dirty state recovery (manually marks version 1 dirty, verifies next open self-heals). CI test job now spins up a `mariadb:11` service container and sets the DSN automatically.

## [0.13.3.0] - 2026-04-01

### Fixed
- **MariaDB/MySQL migrations now run correctly** — the `go-sql-driver/mysql` driver rejects SQL files containing more than one semicolon-separated statement unless `multiStatements=true` is set in the DSN. Migration files (e.g. `001_initial.up.sql`) contain multiple `CREATE TABLE` statements, causing a syntax error on first startup. Migrations now use a separate short-lived connection with `multiStatements=true`; the main connection pool keeps it off to avoid any multi-statement injection risk in the app itself.

## [0.13.2.0] - 2026-04-01

### Fixed
- **golangci-lint config format** — `.golangci.yml` was written in v2 format (`version: "2"`, `linters.default`, `linters.settings`) but the CI action installs v1.x. Rewrote in v1 format: `disable-all: true` replaces `default: none`, `linters-settings` is a top-level key. Linter set is unchanged.

## [0.13.1.0] - 2026-04-01

### Fixed
- **Dirty migration state self-heals on restart** — if a previous container start was interrupted mid-migration (e.g. the database was not fully ready despite the healthcheck), `golang-migrate` marks the schema version as dirty and refuses to start on the next run. thornotes now detects `ErrDirty`, forces back to the last clean version, and retries the failed migration automatically. All `up` migrations use `CREATE TABLE IF NOT EXISTS` so re-running a partially applied migration is safe. Affects both MySQL/MariaDB and SQLite drivers.

## [0.13.0.0] - 2026-04-01

### Added
- **golangci-lint config** — `.golangci.yml` added with an explicit linter set (`errcheck`, `govet`, `staticcheck`, `gosimple`, `ineffassign`, `unused`) using `default: none`. Lint rules are now reproducible locally and in CI regardless of golangci-lint version changes.
- **Multi-arch Docker images** — CI now builds and pushes `linux/amd64` and `linux/arm64` in a single manifest. Self-hosters on Raspberry Pi, ARM NAS devices, or Apple Silicon can pull the native image without emulation.
- **Docker smoke test** — a `smoke-test` CI job runs after `build-push`. It pulls the freshly pushed image, starts a container with a temporary data directory, and verifies HTTP 200 on `/`. The GitHub release job now requires the smoke test to pass before it runs.

## [0.12.2.0] - 2026-04-01

### Changed
- **MariaDB Compose example** — the Docker Compose example now uses `mariadb:11` instead of `mysql:8.0`, with `MARIADB_*` environment variables and the correct `healthcheck.sh` healthcheck.
- **Database connection config split** — `THORNOTES_DB_DSN` is replaced by four discrete variables: `THORNOTES_DB_HOST` (default `localhost:3306`), `THORNOTES_DB_NAME` (default `thornotes`), `THORNOTES_DB_USER`, and `THORNOTES_DB_PASSWORD`. The DSN is assembled internally. Equivalent CLI flags: `--db-host`, `--db-name`, `--db-user`, `--db-password`.

## [0.12.1.0] - 2026-04-01

### Fixed
- **CSP: inline event handlers migrated to `addEventListener`** — since v0.6.0.0 the Content Security Policy has used `script-src 'self'` without `'unsafe-inline'`, but `app.html` still had 33 `onclick`/`onchange`/`oninput` attributes and the tree/token/journal dynamic HTML was injecting `onclick` strings via `innerHTML`. Browsers enforcing the CSP silently blocked every handler, making the entire UI non-interactive. All static inline handlers are now bound via `addEventListener` at the bottom of `app.js`; dynamic handlers use `data-action`/`data-*-id` attributes with event delegation on the container elements.
- **Service worker cache bump** — cache key updated from `thornotes-v0.9.0.0` to `thornotes-v0.12.1.0` so browsers with the stale cached `app.js` pick up the fix immediately on next load.

## [0.12.0.0] - 2026-04-01

### Added
- **Startup reconciliation** — on boot, thornotes now scans every note on disk and updates the DB for any whose content hash has changed (e.g. after a crash or external edit). Previously this scan existed in code but was never wired up at startup.
- **Reconciliation progress logging** — `Reconcile` logs `reconcile: starting` with the total note count, then logs progress every 100 notes (`reconcile: progress {i}/{total}`), then logs `reconcile: complete`. Self-hosters with large corpora will see live progress instead of silence.
- **`--skip-reconciliation` / `THORNOTES_SKIP_RECONCILIATION`** — flag to bypass the startup scan on trusted restarts where it isn't needed, eliminating the delay entirely.

## [0.11.0.0] - 2026-04-01

### Added
- **Disk-full error handling** — when `FileStore.Write()` fails with `ENOSPC`, the server returns HTTP 507 (Insufficient Storage) instead of silently dropping the save. The auto-save handler in the browser detects 507 and shows a persistent red banner: "Your disk is full — note could not be saved." with a dismiss button. Prevents silent data loss for self-hosters with small disks.

## [0.10.0.0] - 2026-04-01

### Added
- **Timezone-aware journal "today"** — `GET /api/v1/journals/{id}/today` now accepts a `?tz=` query parameter (e.g. `?tz=America/New_York`). The server uses `time.LoadLocation` to compute the correct local date for the user. Without the parameter the server falls back to UTC (previous behaviour). The frontend passes the browser's IANA timezone via `Intl.DateTimeFormat().resolvedOptions().timeZone`. Invalid timezone strings return HTTP 400.

## [0.9.0.0] - 2026-04-01

### Added
- **Progressive Web App (PWA)** — thornotes is now installable on desktop and mobile via the browser's "Add to Home Screen" / "Install" prompt
- **PWA manifest** (`/static/manifest.json`) — defines app name, theme colour, icons, standalone display mode, and portrait orientation
- **Service worker** (`/sw.js`) — caches static assets (JS, CSS, fonts, icons) for fast loads and basic offline support; network-first for the app shell, cache-first for static files, network-only for API calls
- **App icons** — SVG icons at 192×192 and 512×512 for home screen and splash screens (`/static/icons/`)
- **Responsive layout** — sidebar collapses off-canvas on screens ≤ 640 px wide; hamburger toggle button in the topbar opens/closes it; a backdrop overlay closes it on tap
- **Touch-friendly tap targets** — all buttons, tree items, and interactive elements raised to ≥ 32–44 px height
- **Safe-area insets** — `env(safe-area-inset-*)` padding applied to body and sidebar so content clears notches and home-indicator bars on iOS/Android
- **Dynamic viewport height** — `height: 100dvh` (with `100vh` fallback) prevents layout being obscured by mobile browser chrome
- **Bottom-sheet modals** — on mobile, modals slide up from the bottom edge with safe-area padding so buttons are not hidden behind the home indicator
- **Auto-close sidebar on note open** — opening a note on mobile automatically closes the sidebar so the editor is immediately visible

## [0.8.0.0] - 2026-04-01

### Changed
- **zerolog replaces `log/slog`** — structured JSON logging (zero-alloc) via `github.com/rs/zerolog`; human-readable console output in development
- **gin replaces stdlib `net/http` mux** — `github.com/gin-gonic/gin` adds panic recovery, per-request access logging (method/path/status/latency/IP), and cleaner route groups
- All handler signatures converted from `(w http.ResponseWriter, r *http.Request)` to `func(c *gin.Context)`
- All middleware (session, bearer, CSRF, rate-limiter, secure headers) converted to `gin.HandlerFunc`
- `SessionMiddleware` and `BearerMiddleware` now return `gin.HandlerFunc` instead of wrapping `http.Handler`

## [0.7.0.0] - 2026-04-01

### Added
- **MySQL support** — set `THORNOTES_DB_DRIVER=mysql` and `THORNOTES_DB_DSN=user:pass@tcp(host:3306)/dbname?parseTime=true` to use MySQL 8.0+ instead of SQLite; all repositories implemented against `database/sql`; migrations embedded in `internal/db/mysql_migrations/`
- Full-text search on MySQL uses a `FULLTEXT` index with `MATCH...AGAINST` in boolean mode (InnoDB)
- Docker Compose with MySQL example added to README
- `THORNOTES_DB_DRIVER` / `--db-driver` and `THORNOTES_DB_DSN` / `--db-dsn` config options

## [0.6.0.0] - 2026-04-01

### Security
- **API tokens now stored as SHA-256 hashes** — raw tokens are returned once on creation and never stored; `GetByToken` hashes before lookup; DB migration 004 renames `token` → `token_hash` and adds `prefix` column for display. Existing tokens are invalidated — regenerate after upgrade.
- **SHA-pinned GitHub Actions** — all third-party actions in CI workflow pinned to immutable commit SHAs (with version tag comments) preventing supply chain attacks via mutable tags
- **`THORNOTES_SECURE_COOKIES` / `--secure-cookies`** — new config option sets the `Secure` flag on session cookies (default `false`; enable when serving over HTTPS)
- **DOMPurify on shared notes** — the public `/s/{token}` share page now sanitizes Markdown-rendered HTML via DOMPurify v3.2.4 (self-hosted) before writing to `innerHTML`, preventing stored XSS via malicious note content
- **CSP `unsafe-inline` removed from `script-src`** — the share page inline script was moved to `web/static/js/share.js`, allowing the CSP to drop `'unsafe-inline'` from `script-src`; inline event-handler injection no longer executes

### Documentation
- Added `THORNOTES_SECURE_COOKIES` to README configuration table and Dockerfile comments
- Added Docker Compose example to README
- Added vibe-coded research disclaimer to README

## [0.5.0.0] - 2026-04-01

### Added
- **Daily journal** — create named journals (e.g. "Personal", "Work") and open today's entry with one click; entries are auto-named `YYYY-MM-DD.md` and filed under `{journal name}/{year}/{month}/`, auto-tagged with "journal entry" and the journal name
- Multiple journals supported; sidebar shows a direct Today button for single journals or a dropdown picker for multiple
- **Getting Started note** — every new user gets a "Getting Started" note in their root folder on registration documenting all app features
- **`GET /api/v1/notes/all`** — new REST endpoint listing all notes across every folder in one call (includes `folder_id` on each item)
- **`folder_id` on note list items** — all listing responses now include `folder_id` so callers know where each note lives without a separate lookup
- **MCP improvements for LLM use** — `list_notes` with no `folder_id` now returns all notes across all folders; `search_notes` accepts a `tags` array filter; `create_note` accepts `tags`; `resources/list` enumerates all notes (not just root); tool descriptions rewritten to be LLM-actionable

### Changed
- `NoteListItem` model now includes `folder_id` field (non-breaking addition)
- MCP `list_notes` with no `folder_id` now returns all notes instead of root-only

## [0.4.0.0] - 2026-04-01

### Added
- Syntax highlighting for fenced code blocks in the editor preview and shared note pages — specify the language after the opening fence (e.g. ` ```go `, ` ```yaml `, ` ```json `) and the preview renders with full colour highlighting
- 180+ languages supported via [highlight.js](https://highlightjs.org/) (self-hosted, no CDN dependency)
- Highlight theme automatically follows the app's dark/light mode: GitHub light in light mode, GitHub Dark in dark mode

## [0.3.0.0] - 2026-03-30

### Added
- Disk watcher — polls `THORNOTES_NOTES_ROOT` every `THORNOTES_WATCH_INTERVAL` (default 30s) for file changes made outside the app (e.g. external editor, `rsync`, git checkout)
- When a file changes on disk, the DB is updated and connected browser tabs receive a `notes_changed` SSE event and auto-refresh the tree and open note
- `GET /api/v1/events` — Server-Sent Events endpoint (session-authenticated); each user has their own event stream
- `internal/hub` — per-user pub/sub hub wiring the watcher to open SSE connections
- `THORNOTES_WATCH_INTERVAL` env var / `--watch-interval` flag — set to `0` to disable the watcher

### Fixed
- Startup `Reconcile()` now covers notes in folders, not just root/unsorted notes (was using `ListByFolder(nil)` which returns root-only; now uses `ListAllForWatch`)

## [0.2.0.0] - 2026-03-29

### Added
- MCP (Model Context Protocol) server at `POST /mcp` — exposes notes as resources and tools for AI assistants (Claude Desktop, Cursor, etc.)
- API token authentication — bearer tokens with `tn_` prefix, managed per-user from the account page
- Account page modal — create/revoke API tokens, view MCP endpoint URL and connection snippet
- `api_tokens` DB table — stores tokens with `name`, `last_used_at` (async background update), and per-user scoping
- MCP tools: `search_notes`, `get_note`, `list_notes`, `create_note`, `update_note`, `list_folders`
- MCP resources: notes exposed as `note://{id}` URIs with `text/markdown` MIME type
- `BearerMiddleware` in `internal/auth` — validates `Authorization: Bearer <token>` header, loads user into request context
- One-time token reveal UI — full token shown only on creation, masked thereafter
- Dark mode support for account modal

## [0.1.0.0] - 2026-03-29

### Fixed
- Missing `GET /api/v1/notes/root` route registration caused 404 on initial load
- Root (unsorted) notes not shown after login — `loadFolderTree` now fetches root notes in parallel with folders
- EasyMDE editor crash (`null.insertBefore`) when textarea was not attached to DOM before initialization
- Share page rendered blank content — `html/template` double-escaped note content in `<script>` context; now embedded via hidden `<pre>` element read with `textContent`
- EasyMDE toolbar icons invisible due to CDN font-awesome blocked by CSP — self-hosted font-awesome 4.7.0, added `font-src 'self'` to CSP header

### Added
- `web/static/css/font-awesome.min.css` and `web/static/fonts/` — self-hosted font-awesome 4.7.0 for CSP compliance
- `internal/security/headers_test.go` — unit tests for `SecureHeaders` middleware covering all response headers
- Initial `VERSION` file
