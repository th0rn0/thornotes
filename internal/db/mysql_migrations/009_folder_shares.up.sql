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
-- token          the bearer capability; uq_folder_shares_token keeps the
--                lookup one indexed equality per request
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
    id             BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id        BIGINT       NOT NULL,
    folder_id      BIGINT       NOT NULL,
    token          VARCHAR(64)  NOT NULL,
    expires_at     DATETIME     NULL DEFAULT NULL,
    last_viewed_at DATETIME     NULL DEFAULT NULL,
    created_at     DATETIME     NOT NULL DEFAULT (UTC_TIMESTAMP()),
    CONSTRAINT fk_folder_shares_user   FOREIGN KEY (user_id)   REFERENCES users(id)   ON DELETE CASCADE,
    CONSTRAINT fk_folder_shares_folder FOREIGN KEY (folder_id) REFERENCES folders(id) ON DELETE CASCADE,
    UNIQUE KEY uq_folder_shares_token (token),
    -- One live share per folder: re-sharing is an UPDATE of this row, so two
    -- live tokens for one folder are not representable, and a re-share cannot
    -- leave a dead token behind that nobody can enumerate.
    UNIQUE KEY uq_folder_shares_folder_id (folder_id),
    INDEX idx_folder_shares_user_id (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
