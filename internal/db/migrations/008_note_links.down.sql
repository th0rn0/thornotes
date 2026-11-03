DROP INDEX IF EXISTS uq_note_links_edge;
DROP INDEX IF EXISTS idx_note_links_to;
DROP INDEX IF EXISTS idx_note_links_from;
DROP TABLE IF EXISTS note_links;
DROP INDEX IF EXISTS idx_notes_title_key;
ALTER TABLE notes DROP COLUMN title_key;
