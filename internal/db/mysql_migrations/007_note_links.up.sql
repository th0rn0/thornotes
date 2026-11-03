-- notes.title_key: normalized lookup key for titles (NFC, entity-decoded,
-- dash-folded, case-folded). Computed in Go; backfilled by link indexing.
-- VARCHAR(512) keeps the (user_id, title_key) index under InnoDB's 3072-byte
-- limit (512 * 4 + 8 = 2056 bytes).
ALTER TABLE notes ADD COLUMN title_key VARCHAR(512) NOT NULL DEFAULT '';
CREATE INDEX idx_notes_title_key ON notes(user_id, title_key);

-- note_links: the resolved link graph. One row per outgoing [[link]] edge.
-- target_hash is sha256(NormalizeKey(target)) — a fixed CHAR(64) so the unique
-- key (from_note_id, target_hash) costs 8 + 256 bytes, far under 3072.
CREATE TABLE IF NOT EXISTS note_links (
    id           BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id      BIGINT       NOT NULL,
    from_note_id BIGINT       NOT NULL,
    to_note_id   BIGINT       NULL,
    raw_target   VARCHAR(512) NOT NULL,
    target_hash  CHAR(64)     NOT NULL,
    anchor       VARCHAR(255) NOT NULL DEFAULT '',
    link_text    VARCHAR(512) NOT NULL DEFAULT '',
    ambiguous    TINYINT(1)   NOT NULL DEFAULT 0,
    resolved_at  DATETIME     NULL,
    created_at   DATETIME     NOT NULL DEFAULT (UTC_TIMESTAMP()),
    CONSTRAINT fk_note_links_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT fk_note_links_from FOREIGN KEY (from_note_id) REFERENCES notes(id) ON DELETE CASCADE,
    CONSTRAINT fk_note_links_to   FOREIGN KEY (to_note_id)   REFERENCES notes(id) ON DELETE SET NULL,
    INDEX idx_note_links_from (from_note_id),
    INDEX idx_note_links_to (to_note_id),
    UNIQUE KEY uq_note_links_edge (from_note_id, target_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
