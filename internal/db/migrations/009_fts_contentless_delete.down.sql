-- Back to the external-content table 001_initial created.
--
-- The index has to be rebuilt either way: this table's rows are a different
-- shape from the contentless one, and 'rebuild' is the only way to fill an
-- external-content table from its content table. Rolling back therefore restores
-- the pre-451 behaviour, including its inability to unindex an edited note
-- without its old values — that is what rolling back means.
DROP TABLE IF EXISTS notes_fts;

CREATE VIRTUAL TABLE notes_fts USING fts5(
    title,
    content,
    content=notes,
    content_rowid=id,
    tokenize='porter ascii'
);

INSERT INTO notes_fts(notes_fts) VALUES('rebuild');
