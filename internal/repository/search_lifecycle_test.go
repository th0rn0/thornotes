package repository_test

// Search lifecycle: what happens to the index when a note changes.
//
// The 452 conformance suite in search_contract_test.go pins what a query returns
// for a fixed set of notes. This file pins the other half — what a query must
// stop returning once the text that matched is gone. Ticket 451 (H-03):
//
//	After a note is edited, a term that no longer appears in its title or
//	content must not match it. After a note is deleted, nothing about it may
//	match anything.
//
// The SQLite index used to fail this because notes_fts is an external-content
// FTS5 table: FTS5's 'delete' command needs the column values *as they were
// indexed*, and SyncNote handed it the note's current values, so tokens from
// text that had been edited away were never unindexed.
//
// Both engines run the same sequence. The MySQL leg is expected to hold before
// the fix as well — InnoDB maintains its FULLTEXT index on UPDATE and DELETE, so
// the engine does the work and the contract is satisfied trivially. It is in
// this file to prove that rather than to assume it, and it is the leg that would
// catch a future change to the MySQL write path.
//
// Every step is asserted twice: through Search (what a user sees) and through
// the engine's own index (what the index holds). The second assertion is the one
// that cannot be faked by a JOIN — Search joins `notes`, so a deleted note's
// orphaned index row is invisible to it.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository/mysql"
	"github.com/th0rn0/thornotes/internal/repository/sqlite"
)

// noteEditor is the write path this contract needs: both dialects expose the
// same three methods with the same signatures.
type noteEditor interface {
	UpdateContent(ctx context.Context, userID, noteID int64, content, contentHash, expectedHash string) error
	Update(ctx context.Context, n *model.Note) error
	Delete(ctx context.Context, userID, noteID int64) error
}

// indexMatches counts the documents the engine's full-text index matches for a
// term, without joining back to the notes table. On SQLite that is a query
// against notes_fts itself; on MySQL it is the FULLTEXT predicate over notes,
// which is the same index a user's search goes through.
type indexMatches func(ctx context.Context, term string) (int, error)

type lifecycleBackend struct {
	*backend
	edits noteEditor
	index indexMatches
}

func newSQLiteLifecycleBackend(t *testing.T) *lifecycleBackend {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "search-lifecycle.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	return &lifecycleBackend{
		backend: &backend{
			name:   "sqlite",
			notes:  sqlite.NewNoteRepo(pool.ReadDB, pool.WriteDB),
			users:  sqlite.NewUserRepo(pool.WriteDB),
			search: sqlite.NewSearchRepo(pool.ReadDB, pool.WriteDB),
		},
		edits: sqlite.NewNoteRepo(pool.ReadDB, pool.WriteDB),
		index: func(ctx context.Context, term string) (int, error) {
			var n int
			err := pool.ReadDB.QueryRowContext(ctx,
				`SELECT count(*) FROM notes_fts WHERE notes_fts MATCH ?`, term).Scan(&n)
			return n, err
		},
	}
}

// newMySQLLifecycleBackend reuses the conformance constructor — including its
// skip-without-a-DSN behaviour and its truncate — and opens one more pool for
// the write path and the index probe.
func newMySQLLifecycleBackend(t *testing.T) *lifecycleBackend {
	t.Helper()
	b := newMySQLBackend(t) // skips the whole test when no DSN is set

	pool, err := db.OpenMySQL(os.Getenv(conformanceDSNEnv))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	return &lifecycleBackend{
		backend: b,
		edits:   mysql.NewNoteRepo(pool.WriteDB),
		index: func(ctx context.Context, term string) (int, error) {
			var n int
			err := pool.ReadDB.QueryRowContext(ctx,
				`SELECT count(*) FROM notes WHERE MATCH(title, content) AGAINST(? IN BOOLEAN MODE)`,
				term).Scan(&n)
			return n, err
		},
	}
}

// mustFind asserts exactly which notes a search returns.
func (b *lifecycleBackend) mustFind(t *testing.T, userID int64, term string, want []string) {
	t.Helper()
	results, err := b.search.Search(context.Background(), userID, term, nil)
	require.NoError(t, err, "%s: search %q", b.name, term)
	require.Equal(t, want, slugsOf(results), "%s: search %q must return %v", b.name, term, want)
}

// mustIndex asserts how many documents the engine's index matches for a term.
func (b *lifecycleBackend) mustIndex(t *testing.T, term string, want int) {
	t.Helper()
	got, err := b.index(context.Background(), term)
	require.NoError(t, err, "%s: index probe %q", b.name, term)
	require.Equal(t, want, got, "%s: the full-text index must match %q in %d documents", b.name, term, want)
}

func TestSearchLifecycle_SQLite(t *testing.T) {
	runLifecycle(t, newSQLiteLifecycleBackend(t))
}

func TestSearchLifecycle_MySQL(t *testing.T) {
	runLifecycle(t, newMySQLLifecycleBackend(t))
}

// runLifecycle drives one note through three edits — content, title, content —
// and then deletes it, asserting after every step that a term the note no longer
// contains has stopped matching and a term it does contain still does. Three
// edits matter: with a single edit a "delete then re-insert with the current
// values" that removes nothing can still look correct, because the surviving
// token happens to be one the note still has.
func runLifecycle(t *testing.T, b *lifecycleBackend) {
	t.Helper()
	ctx := context.Background()

	user, err := b.users.Create(ctx, "lifecycle-user", "hash", "lifecycle-user-uuid")
	require.NoError(t, err, "%s: create user", b.name)

	// A note that is never touched, so "the term stopped matching" cannot be
	// satisfied by an index that stopped matching anything at all.
	_, err = b.notes.Create(ctx, &model.Note{
		UserID: user.ID, Title: "Control Note", Slug: "control-note",
		DiskPath: "lifecycle-user/control-note.md", ContentHash: "hash-control",
		Content: "the narwhal is a separate note",
	})
	require.NoError(t, err, "%s: create control note", b.name)

	note, err := b.notes.Create(ctx, &model.Note{
		UserID: user.ID, Title: "Zebrafish Ledger", Slug: "lifecycle-note",
		DiskPath: "lifecycle-user/lifecycle-note.md", ContentHash: "hash-1",
		Content: "the crossing was repainted last spring",
	})
	require.NoError(t, err, "%s: create note", b.name)

	// ── as created ──────────────────────────────────────────────────────────
	// "zebrafish" is only in the title; "crossing" and "repainted" only in the
	// content.
	b.mustFind(t, user.ID, "zebrafish", []string{"lifecycle-note"})
	b.mustFind(t, user.ID, "crossing", []string{"lifecycle-note"})
	b.mustFind(t, user.ID, "repainted", []string{"lifecycle-note"})
	b.mustIndex(t, "zebrafish", 1)

	// ── edit 1 of 3: the content ────────────────────────────────────────────
	require.NoError(t, b.edits.UpdateContent(ctx, user.ID, note.ID,
		"the okapi wandered past the crossing", "hash-2", "hash-1"), "%s: edit content", b.name)
	b.mustFind(t, user.ID, "okapi", []string{"lifecycle-note"})
	b.mustIndex(t, "okapi", 1)

	// "repainted" was edited out of the content. This is the ticket's symptom
	// in its plainest form: the note no longer contains the word, and the
	// search must not return it.
	b.mustFind(t, user.ID, "repainted", nil)
	b.mustIndex(t, "repainted", 0)

	// ── edit 2 of 3: the title ──────────────────────────────────────────────
	note.Title = "Tapir Ledger"
	note.TitleKey = "tapir ledger"
	require.NoError(t, b.edits.Update(ctx, note), "%s: edit title", b.name)
	b.mustFind(t, user.ID, "tapir", []string{"lifecycle-note"})
	b.mustIndex(t, "tapir", 1)

	// "zebrafish" was in the title and is now in neither column.
	b.mustFind(t, user.ID, "zebrafish", nil)
	b.mustIndex(t, "zebrafish", 0)

	// ── edit 3 of 3: the content again ──────────────────────────────────────
	require.NoError(t, b.edits.UpdateContent(ctx, user.ID, note.ID,
		"the quagga wandered past the crossing", "hash-3", "hash-2"), "%s: edit content again", b.name)
	b.mustFind(t, user.ID, "quagga", []string{"lifecycle-note"})
	b.mustIndex(t, "quagga", 1)

	// "okapi" was the previous edit's term: it must be gone, not merely
	// superseded by the term that replaced it.
	b.mustFind(t, user.ID, "okapi", nil)
	b.mustIndex(t, "okapi", 0)

	// Untouched throughout.
	b.mustFind(t, user.ID, "narwhal", []string{"control-note"})

	// ── deleted ─────────────────────────────────────────────────────────────
	require.NoError(t, b.edits.Delete(ctx, user.ID, note.ID), "%s: delete note", b.name)

	for _, term := range []string{"zebrafish", "tapir", "okapi", "quagga", "crossing", "ledger"} {
		b.mustFind(t, user.ID, term, nil)
		// On MySQL this counts rows in `notes`, and InnoDB drops the row from
		// its FULLTEXT index as part of the DELETE, so 0 here is the engine
		// agreeing rather than a claim about a stale index entry. On SQLite it
		// is the index itself: an external-content FTS5 table keeps its entry
		// for a deleted note for ever, and only the JOIN to `notes` in Search
		// hid that.
		b.mustIndex(t, term, 0)
	}
	b.mustFind(t, user.ID, "narwhal", []string{"control-note"})

	// ── a note deleted while its index entry is stale ───────────────────────
	// Edited but not yet re-synced: the index still holds the text the note used
	// to have, and the note is then deleted. Both the indexed text and the text
	// it was edited to must stop matching.
	stale, err := b.notes.Create(ctx, &model.Note{
		UserID: user.ID, Title: "Pangolin Notes", Slug: "stale-note",
		DiskPath: "lifecycle-user/stale-note.md", ContentHash: "hash-stale-1",
		Content: "pangolin tracks in the garden",
	})
	require.NoError(t, err, "%s: create stale note", b.name)
	b.mustFind(t, user.ID, "pangolin", []string{"stale-note"}) // index it

	require.NoError(t, b.edits.UpdateContent(ctx, user.ID, stale.ID,
		"armadillo tracks in the garden", "hash-stale-2", "hash-stale-1"), "%s: edit stale note", b.name)
	require.NoError(t, b.edits.Delete(ctx, user.ID, stale.ID), "%s: delete stale note", b.name)

	for _, term := range []string{"pangolin", "armadillo"} {
		b.mustFind(t, user.ID, term, nil)
		b.mustIndex(t, term, 0)
	}
	b.mustFind(t, user.ID, "narwhal", []string{"control-note"})
}
