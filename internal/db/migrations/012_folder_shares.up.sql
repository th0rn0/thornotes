-- Folder shares (ticket 616, slice 1): a bearer token pointed at a folder
-- instead of at a single note.
--
-- Two tables, one rule. This one deliberately does NOT hold a nullable
-- note_id: note shares keep living on notes.share_token, and the text both
-- surfaces share is the *expiry rule* (notes.ShareLinkExpired) and the token
-- generator, which are one function each in one package. Migrating every live
-- note token into a union table would rewrite the lookup behind /s/<token> on
-- an unattended deploy path and gain nothing a user can see.
--
-- token          the bearer capability; UNIQUE, so one indexed equality lookup
--                per request and no two folders sharing a link
-- expires_at     when the link stops answering (NULL = never). Nothing writes
--                it yet — expiry is slice 2 — but the column is here so that
--                slice needs no second migration.
-- last_viewed_at the last view recorded for the link, bounded to at most one
--                write per link per ShareViewWindow (internal/notes/share_lifecycle.go)
--
-- There is no revoked_at: revoking is a DELETE, so a revoked link and a folder
-- that was never shared are the same state (no row), exactly as
-- notes.share_token IS NULL is. That also makes it impossible to strand a
-- deadline on an unshared row.
CREATE TABLE IF NOT EXISTS folder_shares (
    id             INTEGER  PRIMARY KEY AUTOINCREMENT,
    user_id        INTEGER  NOT NULL REFERENCES users(id)   ON DELETE CASCADE,
    folder_id      INTEGER  NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
    token          TEXT     NOT NULL UNIQUE,
    expires_at     DATETIME,
    last_viewed_at DATETIME,
    created_at     DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- One live share per folder. Re-sharing is an UPDATE of this row, so two live
-- tokens for one folder are not representable, and a re-share cannot leave a
-- dead token behind that nobody can enumerate.
CREATE UNIQUE INDEX IF NOT EXISTS idx_folder_shares_folder_id ON folder_shares(folder_id);
CREATE INDEX IF NOT EXISTS idx_folder_shares_user_id ON folder_shares(user_id);
