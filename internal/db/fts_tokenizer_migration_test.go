package db

// Migration 010 replaces notes_fts with a `porter unicode61` table. What has to
// be true of it, and what this file says so:
//
//   - on a fresh database the table carries the new tokenizer, still in the
//     contentless-delete shape 009 introduced — 010 changes the tokenizer and
//     nothing else;
//   - on a database that already has rows, the index is rebuilt from `notes`
//     inside the migration, so the notes that were there before it ran are
//     searchable by their accented text immediately afterwards.
//
// The second case is the one that matters and the one that is easy to get wrong:
// FTS5 has no ALTER for its tokenizer, so the migration drops the table and
// creates it again. A recreate that forgets the repopulate passes every
// fresh-install test and leaves an existing vault's index empty — the vault still
// has its notes, but search finds nothing until each note is somehow re-synced,
// and a note that is never edited is never re-synced (ticket 626).

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigration010_FreshDatabaseUsesUnicode61(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	pool, err := Open(path)
	require.NoError(t, err)
	defer pool.Close()

	version, maxSupported := pool.SchemaStatus()
	assert.Equal(t, 10, version)
	assert.Equal(t, 10, maxSupported)

	schema := ftsSchema(t, pool.ReadDB)
	assert.Contains(t, schema, "tokenize='porter unicode61'",
		"notes_fts must be indexed with the accent-folding tokenizer")
	assert.NotContains(t, schema, "porter ascii", "the ascii tokenizer is what could not fold accents")
	assert.Contains(t, schema, "porter", "porter must still be chained: stemming is not what changed")
	assert.Contains(t, schema, "contentless_delete=1", "010 must not undo 009's contentless-delete shape")
	assert.Contains(t, schema, "content=''", "a contentless-delete table stores no copy of the note text")
}

// TestMigration010_PopulatedDatabaseIsReindexed is the acceptance test for the
// repopulate. It builds a database the way a release leaves one — version 9,
// notes already in the table, index already populated with the ascii tokenizer —
// and then runs the upgrade the container start performs.
func TestMigration010_PopulatedDatabaseIsReindexed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	// A database left behind by the previous release: schema 9, `porter ascii`.
	migrateScratchDBTo(t, path, 9)

	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	require.NoError(t, err)

	preSchema := ftsSchema(t, raw)
	require.Contains(t, preSchema, "tokenize='porter ascii'", "precondition: the old tokenizer")

	_, err = raw.Exec(`INSERT INTO users (username, password_hash, uuid) VALUES ('alice', 'hash', 'uuid-alice')`)
	require.NoError(t, err)

	// Four notes whose accented text is the only place the query could match: the
	// titles are plain ASCII and contain none of the queried words, so a match
	// after the migration can only come from the accented content being indexed
	// with a tokenizer that folds it.
	notes := []struct {
		id           int64
		title        string
		slug         string
		content      string
		accentedTerm string
		asciiQuery   string
	}{
		{1, "Corner Table", "corner-table", "we met at the café on the corner", "café", "CAFÉ"},
		{2, "Token Argument", "token-argument", "a naïve assumption about tokens", "naïve", "naive"},
		{3, "Spring Trip", "spring-trip", "İstanbul in the spring", "İstanbul", "istanbul"},
		{4, "Everything", "everything", "über alles", "über", "uber"},
	}
	for _, n := range notes {
		_, err = raw.Exec(`
			INSERT INTO notes (id, user_id, title, slug, disk_path, content, content_hash, tags, fts_synced_at)
			VALUES (?, 1, ?, ?, ?, ?, ?, '[]', datetime('now'))`,
			n.id, n.title, n.slug, "alice/"+n.slug+".md", n.content, "hash-"+n.slug)
		require.NoError(t, err)
	}

	// The index as the previous release left it. fts_synced_at is set, so nothing
	// would re-sync these notes after the upgrade: whatever the migration does to
	// the index is what search will use for ever.
	_, err = raw.Exec(`INSERT INTO notes_fts(rowid, title, content) SELECT id, title, content FROM notes`)
	require.NoError(t, err)

	require.Equal(t, len(notes), ftsRowCount(t, raw), "precondition: every note is indexed")

	// The defect, on the populated database, before the upgrade: the accented text
	// is indexed but unfindable, because the ascii tokenizer kept `café` as a
	// token distinct from `café`'s ASCII form.
	for _, n := range notes {
		assert.Equal(t, 0, ftsMatchCount(t, raw, `"`+n.asciiQuery+`"`),
			"precondition: %q must not match %q under porter ascii", n.asciiQuery, n.accentedTerm)
	}
	require.NoError(t, raw.Close())

	// The upgrade, exactly as the container start performs it.
	pool, err := Open(path)
	require.NoError(t, err)
	defer pool.Close()

	version, _ := pool.SchemaStatus()
	require.Equal(t, 10, version)

	// The repopulate. A migration that recreated the table and forgot this line
	// would leave the index empty and every assertion below would fail.
	assert.Equal(t, len(notes), ftsRowCount(t, pool.ReadDB),
		"the migration must re-index every note that was already in the table")

	for _, n := range notes {
		assert.Equal(t, 1, ftsMatchCount(t, pool.ReadDB, `"`+n.asciiQuery+`"`),
			"after the migration %q must find the note whose text holds %q", n.asciiQuery, n.accentedTerm)
	}

	// Still one document per note and not two: DROP + CREATE started from an empty
	// table, so there is no chance of a double-indexed note, but a repopulate that
	// ran twice would show up here.
	assert.Equal(t, len(notes), ftsRowCount(t, pool.ReadDB))
}

// ftsRowCount counts the documents in notes_fts without depending on a term, so
// "every existing note is in the index" can be asserted directly.
func ftsRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM notes_fts`).Scan(&n))
	return n
}
