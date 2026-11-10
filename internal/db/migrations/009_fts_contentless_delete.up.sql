-- notes_fts: external content → contentless-delete (ticket 451, H-03).
--
-- An external-content FTS5 table cannot unindex a row without the column values
-- *as they were indexed*: its 'delete' command removes the tokens of the values
-- it is handed. The sync path only ever had the note's current values, so each
-- edit added the new tokens and removed only those of the new text — the tokens
-- belonging to the old text had nothing to remove them and stayed searchable for
-- ever.
--
-- A contentless-delete table keeps what FTS5 needs to unindex a row itself, so
-- the sync path can say `DELETE FROM notes_fts WHERE rowid = ?` and be right by
-- construction. It stores no copy of the note text: correctness for a deleted
-- row comes from a tombstone (rowid) attached to the segment holding its tokens,
-- which queries ignore immediately and merges reclaim later.
--
-- The table is dropped and recreated rather than altered — FTS5 has no ALTER for
-- its content option — and the rebuild is part of the point: every stale token
-- accumulated before this migration is dropped by indexing the current text and
-- nothing else. The 'rebuild' command is unavailable on a contentless table, so
-- the index is filled from `notes` directly.
DROP TABLE IF EXISTS notes_fts;

CREATE VIRTUAL TABLE notes_fts USING fts5(
    title,
    content,
    content='',
    contentless_delete=1,
    tokenize='porter ascii'
);

INSERT INTO notes_fts(rowid, title, content)
SELECT id, title, content FROM notes;
