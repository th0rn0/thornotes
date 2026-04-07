# Changelog

All notable changes to thornotes are documented here.

## [1.5.6.0] - 2026-04-07

### Added
- **Settings modal** — a gear icon in the topbar opens a dedicated Settings panel. Theme selection moves here, giving the topbar a cleaner look and making room for future settings.
- **Auto-collapse sidebar** — folders automatically collapse after 30 seconds of inactivity. Toggleable in Settings > Sidebar. Resets on any mouse, keyboard, or touch activity.
- **Three new themes** — Nord, Tokyo Night, and Solarized Light. All three have full CSS variable coverage including editor, sidebar, modals, code blocks, and notifications.

### Changed
- Theme selector removed from topbar; now lives in the Settings modal under Appearance.

### Added (developer)
- MySQL integration test suite (`internal/repository/mysql/repos_test.go`) — run against a real MySQL server by setting `THORNOTES_MYSQL_TEST_DSN`. All tests skip gracefully without the env var.
- Comprehensive test coverage improvements across `db`, `handler`, `notes`, `apperror`, and `repository/sqlite` packages.

## [1.5.5.1] - 2026-04-05

### Changed
- **Preview Edit live render** — typing in the inline editor now updates the rendered preview instantly on every keystroke. The raw markdown textarea sits above a live output panel (blue border) so you see formatted output as you type — `##` becomes an `<h2>` the moment you add the space.

## [1.5.5.0] - 2026-04-05

### Added
- **Table size picker** — clicking the Table toolbar button now opens an 8×8 grid picker. Hover to select dimensions, click to insert a table of that size. Tabular selections still convert to Markdown tables directly. Dismiss with Escape, click-outside, or a second click on the button.
- **Preview Edit mode** (`Pedit` toolbar button) — renders the note as a full-width preview. Click any block (paragraph, heading, list, code fence, table, etc.) to replace it with an inline textarea pre-filled with that block's raw Markdown. `Ctrl+Enter` or click-away saves and re-renders; `Escape` cancels. Auto-saves on commit. Mode is persisted to `localStorage` across sessions.

## [1.5.4.2] - 2026-04-05

### Changed
- Disabled Wails CI job temporarily while webkit2gtk dependency install is stabilised.

## [1.5.4.1] - 2026-04-05

### Added
- **Wails desktop app** — Go-native alternative to the Electron wrapper (`desktop-wails/`). Uses OS native webview, system tray, and config persistence in `~/.config/thornotes-wails/`. ~10–15 MB vs ~150 MB for Electron.
- **`docker-compose.yml`** — local MariaDB 11 service matching CI credentials exactly. `make test-db-up` starts it, `make test-with-db` runs the full Go test suite against it.
- **Wails config persistence tests** — `desktop-wails/internal/config` package with `Load`/`Save` functions and 11 new tests covering: missing file, valid JSON, invalid JSON, unknown fields, round-trip, directory creation, overwrite, and empty config.
- **Full Wails CI build** — `test-wails` job installs webkit2gtk + builds with CGO + runs `go test -race ./...` with coverage report. Pinned to `ubuntu-22.04`.

### Fixed
- **Line number toggle** — the `#` toolbar button now correctly shows and hides line numbers. The CM6 bundle was missing `lineNumbers()` in its extensions, so the gutter DOM elements were never rendered and the CSS toggle had nothing to act on.

## [1.5.4.0] - 2026-04-04

### Added
- **Desktop app** — Electron wrapper (`desktop/`) connects to any thornotes server (local or remote). Includes setup screen, system tray with context menu, config persistence in OS user-data directory, and graceful fallback to setup on connection failure.
- **Split view** — new toolbar button shows editor and live preview side-by-side (stacks vertically on mobile). View mode persisted to `localStorage`.
- **Insert Markdown table** — toolbar button inserts a blank GFM table template, or converts selected tabular text to a Markdown table.
- **CSV/Excel paste detection** — pasting tabular content (TSV from Excel, RFC 4180 CSV) shows an inline conversion bar offering to reformat as a Markdown table.
- **Right-click "Make into table"** — selecting text and right-clicking the editor shows a context menu option to convert selected delimited content to a Markdown table.

### Tests
- `web/static/js/table-utils.test.js` — 31 JS unit tests for CSV/TSV parsing and Markdown table generation (using Node built-in `node:test`).
- `desktop/lib.test.js` — 31 JS unit tests for desktop URL validation and config merge helpers.

### CI
- New `test-js` job runs all JS unit tests on every push.
- New `build-desktop` job syntax-checks all desktop sources (`node --check`).
- Go `test` job now emits a coverage summary (`go tool cover -func`).
- `build-push` now requires all four CI jobs to pass before proceeding.

## [1.5.3.1] - 2026-04-04

### Added
- **Logo mark in topbar** — the app icon now appears alongside the "thornotes" wordmark in the top bar.

### Changed
- **Refined app icon** — `icon-192.svg` and `icon-512.svg` updated to a clean teardrop/flame mark on a dark background, replacing the "tn" text icon.

### Fixed
- **Folder select clears active note** — switching to a folder now resets `currentNote` so stale note state can't bleed into the folder view.
- **Note open clears active folder** — opening a note now resets `currentFolderId`, keeping folder and note selection mutually exclusive.

## [1.5.3.0] - 2026-04-03

### Fixed
- **No spurious auto-save on note open** — opening a note no longer immediately triggers an auto-save. A `_loadingNote` flag suppresses the CodeMirror `updateListener` while content is loaded programmatically, so the save timer only fires on genuine user edits.

### Tests
- Added 29 integration tests covering: move note, move folder, import handler, PATCH-returns-slug, and account token CRUD.
- `UpdateNoteMetadata` service tests verify slug recomputation and disk path rename on title change.

## [1.5.2.0] - 2026-04-03

### Changed
- **URL updates when note title changes** — renaming a note in the titlebar now updates the browser URL to reflect the new slug immediately, via `history.replaceState`. The PATCH response now returns the server-computed slug and title so the client stays in sync.
- **Faster sidebar update on title rename** — the in-memory note list (`notesByFolder` / `rootNotes`) is updated immediately before `renderTree()` is called, so the sidebar reflects the new title without waiting for a full tree reload.

## [1.5.1.0] - 2026-04-03

### Added
- **Rename note from context menu** — right-clicking a note in the sidebar now shows a Rename option (alongside Open and Delete). Prompts for a new title, PATCHes the note, and updates the titlebar and tree immediately.
- **Delete key shortcut** — pressing Delete when a note is open (and no text field or the editor has focus) confirms and deletes the current note. Safe: the shortcut is suppressed when the cursor is inside the CM6 editor, the title input, the tags field, or any other text input.

## [1.5.0.0] - 2026-04-03

### Added
- **Full CRUD MCP tools** — 7 new write tools expand the MCP server from read-heavy to fully operational:
  - `rename_note` — update a note's title and/or tags without touching content
  - `move_note` — move a note to a different folder or to root
  - `delete_note` — permanently delete a note and its `.md` file
  - `create_folder` — create a folder (optionally nested)
  - `rename_folder` — rename a folder; all descendant disk paths updated atomically
  - `move_folder` — reparent a folder; circular moves are rejected
  - `delete_folder` — delete a folder and all its contents
- **Detailed tool descriptions** — every MCP tool now carries a rich description: what it returns, field names and types, common workflows, constraints, and tips. Makes the tools self-documenting for any LLM.
- All new write tools respect the API token read/write scope introduced in v1.4.0.0.

## [1.4.1.0] - 2026-04-03

### Added
- **Line count in status bar** — the editor footer now shows character count and line count (e.g. `1 234 chars · 42 lines`).
- **Line numbers toggle** — a `#` button in the editor toolbar shows or hides line numbers. Preference is persisted in `localStorage`.
- **MCP client setup in Account modal** — the Account modal now includes collapsible setup guides for Claude Desktop (dynamic JSON config with a Copy button) and Open WebUI (step-by-step with Docker host note).

## [1.4.0.0] - 2026-04-03

### Added
- **Import** — new `POST /api/v1/import` endpoint and sidebar "↑ Import" button. Accepts a single `.md` file (imported as a root-level note) or a `.zip` archive of Markdown files (folders in the ZIP become folders in thornotes, up to 10 MB). Duplicate note titles in the same folder are silently skipped.
- **Folder right-click context menu** — right-clicking a folder in the sidebar now shows a context menu with Rename and Delete actions, mirroring the existing note context menu.
- **UUID-based disk paths** — user data directories are now stored under a UUID (`notes/<uuid>/`) rather than the integer database ID. A startup migration backfills UUIDs for existing users and renames their directories atomically.
- **API token scopes** — API tokens used for MCP can now be created with "Read + Write" (default) or "Read only" scope. The scope badge is shown in the token list. Read-only tokens reject write tools (`create_note`, `update_note`) with a `403` response.
- **Descriptive error messages** — timezone errors now include an example IANA timezone name; invalid ID parameters report the expected format; all service layer errors carry human-readable messages.

## [1.3.0.0] - 2026-04-03

### Added
- **Wiki-style note links** — type `[[Note Title]]` in any note to link to another note. In the preview pane, resolved links are clickable and open the target note immediately. Unresolved links render in a muted style. Links on share pages render as plain text. The link resolver builds a title map from all currently loaded notes (root + expanded folders).
- **Folder overview** — clicking a folder in the sidebar now opens a card grid in the main pane showing all notes in that folder. Each card displays the note title, tags, and a content snippet (first 200 characters, lazily fetched in parallel for up to 20 notes). Clicking a card opens the note.
- **Rename "Unsorted" → "Root"** — the catch-all section at the bottom of the sidebar is now labelled "Root" throughout the UI, comments, and documentation. Notes without a folder live in Root; dragging a note to Root clears its folder assignment.

## [1.2.0.0] - 2026-04-03

### Added
- **Multi-theme system** — replaces the dark/light toggle with a 4-option dropdown: Auto, Light, Dark, Catppuccin Mocha. Auto tracks OS preference and live-switches when it changes. All 107 `body.dark` overrides refactored into ~50 CSS custom properties. Catppuccin Mocha includes matching editor colors (CM6) and syntax highlighting (`highlight-catppuccin-mocha.min.css`). No FOUC: theme is resolved and applied in a blocking `<head>` script before first paint, including the hljs CSS href swap.

## [1.1.1.0] - 2026-04-03

### Added
- **Dark mode on shared note preview** — the `/s/:token` share page now respects the visitor's OS dark mode preference. Uses `prefers-color-scheme` media queries: dark background (`#1a1a1a`), light text, adapted code blocks and blockquotes. Syntax highlighting swaps to `highlight-github-dark.min.css` via CSS `media` attribute — no JS, no flash.

## [1.1.0.0] - 2026-04-03

### Added
- **Move notes and folders** — drag or right-click to move any note into a folder (or back to the root). Folders can be reparented to any other folder (with circular-reference protection). Full cascade: moving a folder renames all descendant disk paths in one transaction. File is moved on disk before DB update; disk rename rolls back automatically on DB failure.

### Security
- **Path traversal fix (HIGH)** — folder names containing `..` or `/` are now rejected at the service layer in `CreateFolder` and `RenameFolder`. `filepath.Join("1", "../2")` evaluates to `"2"` in Go, which mapped attacker user_id=1's folder into user_id=2's root directory. The previous `safePath` guard only prevented escaping `notesRoot` entirely; it did not block cross-user traversal within it. Fix: `filepath.Base(name) != name || name == ".." || name == "."` guard added to both operations. Covers `CreateJournal` automatically (it calls `CreateFolder` internally).

## [1.0.0.0] - 2026-04-02

### Changed
- **v1.0** — first stable release.

## [0.19.4.0] - 2026-04-02

### Changed
- **Test coverage** — pushed line coverage from 64.4% → 70.5% (+920 lines of tests across 11 files). New coverage spans `apperror`, `auth` BearerMiddleware, `config` envDuration, handler account/events/history/share unit tests, `notes` git commit-delete/rename paths, `notes` NoteContext, and `security` CSRF gin middleware + rate limiter gin middleware.

## [0.19.3.0] - 2026-04-02

### Fixed
- **bcrypt cost in tests** — `auth.NewService` uses bcrypt cost 12 (production default). On GitHub Actions 2-core runners this takes ~1.5 s/hash; with 136 handler tests each performing register+login that pushed the suite past the 10-minute CI timeout. Added `NewServiceForTest(users, sessions, allowRegistration bool)` that uses `bcrypt.MinCost` (cost 4) and updated all test helpers in `internal/auth`, `internal/handler`, and `internal/router` to use it.

## [0.19.2.0] - 2026-04-02

### Fixed
- **AuthRateLimiter goroutine leak** — `cleanupLoop` used `for range ticker.C` which blocks forever (stopping a ticker does not close its channel). Every test client created a new `AuthRateLimiter`, leaking one goroutine per test. With 70+ MCP handler tests this accumulated thousands of idle goroutines, degrading the Go scheduler enough to push the handler test suite past the 10-minute timeout in CI. Added a `Stop()` method that closes a `stopCh` channel and rewrote `cleanupLoop` to `select` on both `ticker.C` and `stopCh`. Wired `t.Cleanup(rateLimiter.Stop)` into all test helpers and `rateLimiter.Stop()` into the main graceful shutdown.

### Changed
- **README** — updated editor description from EasyMDE to CodeMirror 6; refreshed screenshots.

## [0.19.1.0] - 2026-04-02

### Fixed
- **Service worker stale cache** — the PWA service worker cache name was stuck at `v0.13.6.0` since the CodeMirror 6 migration (v0.16.0.0). Browsers with the app installed as a PWA received a fresh `app.html` (network-first) that loads `codemirror6.min.js`, but the cached `app.js` still called `new EasyMDE()`, causing the editor to fail silently. Bumped cache to `v0.19.0.0`, swapped EasyMDE assets for `codemirror6.min.js`, and removed `purify.min.js` (no longer referenced).
- **golangci-lint CI timeout** — added `run: timeout: 5m` to `.golangci.yml`. The go-git transitive dependency tree pushed lint analysis past the default 1-minute limit on cold CI runners (observed: ~78s → timeout).

## [0.19.0.0] - 2026-04-02

### Added
- **Version history UI** — a "History" button appears in the editor titlebar when a note is open. Clicking it opens a modal showing up to 50 past git commits for that note (newest first), with timestamps formatted as relative time (e.g. "5m ago"). Selecting an entry loads the note content at that commit in a read-only preview pane. A "Restore this version" button replaces the current note content with the selected version and commits the restoration to history. When git history is not enabled, the modal shows a friendly message with the flag to use. Fully dark-mode aware with mobile-responsive layout (single-column on narrow screens).

## [0.18.0.0] - 2026-04-02

### Added
- **Git-backed version history** — every note save, delete, and folder rename is recorded as a git commit in the notes directory when `--enable-git-history` / `THORNOTES_ENABLE_GIT_HISTORY=true` is set. Uses [go-git](https://github.com/go-git/go-git) (pure Go, no git binary required). A `.gitignore` for thornotes temp files is written on first run.
  - `GET /api/v1/notes/:id/history` — list commits for a note, newest first. Optional `limit` query param (default 50, 0 = unlimited).
  - `GET /api/v1/notes/:id/history/:sha` — retrieve a note's content at a specific commit.
  - `POST /api/v1/notes/:id/history/:sha/restore` — restore a note to a past commit (requires `content_hash` body field for optimistic concurrency). The restoration is itself committed to history.
  - All three endpoints return HTTP 501 when git history is not enabled.

## [0.17.0.0] - 2026-04-02

### Added
- **Deep linking** — note URLs now reflect the folder path and note slug (e.g. `/My-Folder/my-note`). Navigating directly to a deep link or refreshing the page opens the correct note. Browser back/forward work across note navigation. The app shell is served for all non-API paths so deep links survive a hard refresh. `document.title` updates to show the open note name.

## [0.16.0.0] - 2026-04-02

### Changed
- **CodeMirror 6 editor** — replaced EasyMDE (CodeMirror 5, 320KB) with a CodeMirror 6 setup (`@codemirror/view`, `@codemirror/state`, `@codemirror/commands`, `@codemirror/lang-markdown`, `@codemirror/language`). Bundled with bun into `vendor/codemirror6.min.js` (493KB). Features: markdown syntax highlighting in the editor, line wrapping, full undo/redo via CM6 history, custom VS Code-dark and light themes with live switching, soft keyboard–friendly touch interaction.
- **New toolbar** — replaced Font Awesome icon toolbar with a clean text-button toolbar: Bold, Italic, H#, Blockquote, Bullet List, Numbered List, Link, Preview toggle, Undo, Redo. All formatting commands operate on the current selection or insert at cursor.
- **Preview toggle** — "Preview" toolbar button switches the editor area to a rendered markdown view (using the existing `marked` + `highlight.js` pipeline). Clicking again returns to the raw editor. Preview updates live while typing when open.
- **Dark mode** — CM6 theme switches immediately when the dark mode toggle is flipped via `EditorView.dispatch` with a `Compartment` reconfigure, matching the existing `#1e1e1e` / `#d4d4d4` VS Code palette.
- **Build target** — `make build-cm6` rebuilds the vendor bundle from `web/cm6-bundle/index.js` using bun.

## [0.15.0.0] - 2026-04-02

### Added
- **LLM context endpoint** — `GET /api/v1/notes/context` returns all of a user's note content concatenated into a single markdown string, ready to paste into an LLM prompt. Optional `folder_id` query parameter restricts output to a single folder. Response: `{ context, note_count, truncated, char_limit }`. Truncates at 200,000 characters (oldest notes first, newest preserved). Also exposed as MCP resource `notes://context`.

## [0.14.1.0] - 2026-04-01

### Fixed
- **Notes root writable check at startup** — `NewFileStore` now creates a temporary probe file in the notes directory immediately after `MkdirAll`. If the directory exists but is read-only (e.g. a read-only bind mount, wrong ownership, `chmod 555`) the process exits with a clear `notes root "..." is not writable` error instead of starting successfully and failing silently on the first save.

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
