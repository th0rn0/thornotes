# Changelog

All notable changes to thornotes are documented here.

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
