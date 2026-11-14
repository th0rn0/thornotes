-- notes_fts: porter ascii → porter unicode61 (ticket 626).
--
-- The index agreed with MariaDB on ASCII text, on punctuation and on short
-- terms, and disagreed on accents — and on accents SQLite was the side that
-- missed. FTS5's `ascii` tokenizer folds ASCII case only, so it indexed `café`
-- as a token distinct from `cafe`/`CAFÉ`: a query for CAFÉ returned nothing while
-- MariaDB's utf8mb4_unicode_ci found the note. Development runs SQLite, so
-- development looked worse than production, and a self-hoster on SQLite had a
-- search that silently found nothing.
--
-- `unicode61` is the tokenizer that folds those accents (it decomposes and strips
-- combining marks, and case-folds beyond ASCII), and `porter` chains on top of it
-- exactly as before: the stemming is unchanged, only the base tokenizer moves.
--
-- One rule it does not reproduce: `ß` → `ss`. unicode61 folds by decomposition
-- and `ß` has none, so a query for STRASSE still does not find `Straße` on
-- SQLite, while MariaDB's collation does. Documented, not fixed here — no FTS5
-- tokenizer offers a ß rule.
--
-- Every existing entry is rebuilt: FTS5 has no ALTER for its tokenizer, so the
-- table is dropped and recreated, and the index is filled from `notes` itself.
-- `rebuild` is unavailable on a contentless table (009 discovered the same), so
-- the repopulate is an explicit INSERT ... SELECT. That repopulate is the part
-- that must not be lost: a recreate that forgets it leaves every existing note
-- unsearchable until something re-syncs it, which for a settled vault is never.
DROP TABLE IF EXISTS notes_fts;

CREATE VIRTUAL TABLE notes_fts USING fts5(
    title,
    content,
    content='',
    contentless_delete=1,
    tokenize='porter unicode61'
);

INSERT INTO notes_fts(rowid, title, content)
SELECT id, title, content FROM notes;
