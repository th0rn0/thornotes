-- Back to `porter ascii`, the tokenizer 009 left in place.
--
-- Same drop/recreate/repopulate as the up migration, for the same reason: the
-- tokenizer is a property of the table, so the table has to be rebuilt, and a
-- contentless table cannot be filled with `rebuild`. Rolling back therefore
-- restores the accent-blind index — a query for CAFÉ stops finding `café` again.
-- That is what rolling back means.
--
-- Down migrations here are structurally tested and not run by the application:
-- the schema preflight refuses a database newer than the build rather than
-- downgrading it. This file exists so the pair is complete and so an operator
-- with a backup has a documented way back.
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
