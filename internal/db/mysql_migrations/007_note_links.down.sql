DROP TABLE IF EXISTS note_links;
ALTER TABLE notes DROP INDEX idx_notes_title_key;
ALTER TABLE notes DROP COLUMN title_key;
