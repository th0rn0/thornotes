-- notes.title_key: normalized lookup key for titles (NFC, entity-decoded,
-- dash-folded, case-folded). Computed in Go because SQL cannot NFC-normalize;
-- backfilled by the link-indexing pass and kept fresh on create/rename.
ALTER TABLE notes ADD COLUMN title_key TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_notes_title_key ON notes(user_id, title_key);

-- note_links: the resolved link graph. One row per outgoing [[link]] edge.
-- target_hash is sha256(NormalizeKey(target)) — dialect-neutral, index-safe,
-- and answerable before any normalization difference leaks in.
CREATE TABLE IF NOT EXISTS note_links (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    from_note_id INTEGER NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
    to_note_id   INTEGER REFERENCES notes(id) ON DELETE SET NULL,
    raw_target   TEXT    NOT NULL,
    target_hash  TEXT    NOT NULL,
    anchor       TEXT    NOT NULL DEFAULT '',
    link_text    TEXT    NOT NULL DEFAULT '',
    ambiguous    INTEGER NOT NULL DEFAULT 0,
    resolved_at  DATETIME,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_note_links_from ON note_links(from_note_id);
CREATE INDEX IF NOT EXISTS idx_note_links_to   ON note_links(to_note_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_note_links_edge
    ON note_links(from_note_id, target_hash);
