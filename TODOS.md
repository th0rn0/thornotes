# thornotes — TODOS

## V1 (in-scope, implement before shipping)

### Rate limiter: X-Forwarded-For + --trusted-proxy flag
**What:** The per-IP rate limiter uses `r.RemoteAddr` directly. Behind a reverse proxy
(nginx/Caddy — the recommended deployment), this is always the proxy's IP. Every user
appears to be the same IP and gets throttled together.

**Why:** Rate limiting is a stated security requirement. Behind a proxy it doesn't work.

**How to apply:** Only trust `X-Forwarded-For` when `--trusted-proxy` CIDR is set.
Default: trust nothing (direct connections only). When configured: extract the leftmost
non-trusted IP from XFF chain.

**Where:** `internal/security/ratelimit.go` + `internal/config/config.go` (`TrustedProxy` field)

**Depends on:** config system, rate limiter implementation

---

### Lazy-loading note list: GET /api/v1/folders/{id}/notes
**What:** Add endpoint to load note metadata per-folder. Remove note metadata from
`GET /api/v1/folders` response (folders return tree structure only).

**Why:** Full-tree load with note metadata doesn't scale for hosted service with power
users (10,000+ notes). Lazy-loading per folder keeps initial load fast.

**API change:**
```
GET /api/v1/folders           → [{ id, name, parent_id, child_count, note_count }]
GET /api/v1/folders/{id}/notes → [{ id, title, slug, updated_at, tags }]
```

**Frontend change:** Track `loadedFolderIds` in JS state. On folder expand: fetch
notes if not yet loaded. Show loading indicator during fetch.

**Where:** `internal/handler/folders.go`, `web/static/js/app.js`

**Depends on:** folder and note handlers

---

## V2 (deferred, design in V1 for easy addition)

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
