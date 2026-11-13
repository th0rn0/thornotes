package repository_test

// fts_synced_at: what the write paths must do to the "this note's index entry is
// stale" marker (ticket 472, bullet 1).
//
// search_lifecycle_test.go pins the *observable* half of the FTS lifecycle — a
// word edited out of a note stops matching, a deleted note leaves nothing in the
// index. This file pins the mechanism those behaviours depend on, as a column:
// every write that changes indexed text (the body, or the title, which is
// indexed too) must clear fts_synced_at, so the next search re-indexes the note
// instead of trusting a stamp that describes text the note no longer has.
//
// The assertions are on the column and not on a search result on purpose. A
// search-level assertion cannot tell "the stamp was cleared and the note was
// re-indexed" from "the engine happened to drop the old tokens anyway" — on
// MySQL, InnoDB maintains its FULLTEXT index on UPDATE, so the search half of
// this contract holds even if the column were never cleared. That is exactly the
// kind of accident that makes an untested path look certified, so the column is
// read back directly (typed through GetByID, and once as raw SQL so a zero-time
// pointer cannot stand in for a NULL).
//
// Both backends run the same three writes: Update (a rename), UpdateMetadataAndContent
// (a rename that also moves the body), UpdateContent (a body edit).

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository/mysql"
	"github.com/th0rn0/thornotes/internal/repository/sqlite"
)

// ftsNoteWriter is the write surface this contract is about. Both dialects
// expose it with identical signatures, so one test drives both.
type ftsNoteWriter interface {
	Create(ctx context.Context, n *model.Note) (*model.Note, error)
	GetByID(ctx context.Context, userID, noteID int64) (*model.Note, error)
	Update(ctx context.Context, n *model.Note) error
	UpdateMetadataAndContent(ctx context.Context, n *model.Note) error
	UpdateContent(ctx context.Context, userID, noteID int64, content, contentHash, expectedHash string) error
}

type ftsSearcher interface {
	Search(ctx context.Context, userID int64, query string, tags []string) ([]*model.SearchResult, error)
}

type ftsStampBackend struct {
	name   string
	notes  ftsNoteWriter
	search ftsSearcher
	// setStamp writes fts_synced_at directly, standing in for the state the
	// sync path leaves behind. It is the only way to reach that state on MySQL,
	// where SyncNote is a deliberate no-op (InnoDB needs no sync step) and no
	// production path ever writes a non-NULL stamp.
	setStamp func(ctx context.Context, noteID int64) error
	// rawStamp reads the column with SQL, not through the model: a NULL scan
	// into *time.Time gives nil, but only a raw read proves the stored value is
	// NULL rather than a zero timestamp.
	rawStamp func(ctx context.Context, noteID int64) (sql.NullString, error)
	users    interface {
		Create(ctx context.Context, username, passwordHash, uuid string) (*model.User, error)
	}
}

func newSQLiteFTSStampBackend(t *testing.T) *ftsStampBackend {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "fts-stamp.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	return &ftsStampBackend{
		name:   "sqlite",
		notes:  sqlite.NewNoteRepo(pool.ReadDB, pool.WriteDB),
		search: sqlite.NewSearchRepo(pool.ReadDB, pool.WriteDB),
		users:  sqlite.NewUserRepo(pool.WriteDB),
		setStamp: func(ctx context.Context, noteID int64) error {
			_, err := pool.WriteDB.ExecContext(ctx,
				`UPDATE notes SET fts_synced_at = datetime('now') WHERE id = ?`, noteID)
			return err
		},
		rawStamp: func(ctx context.Context, noteID int64) (sql.NullString, error) {
			var got sql.NullString
			err := pool.ReadDB.QueryRowContext(ctx,
				`SELECT fts_synced_at FROM notes WHERE id = ?`, noteID).Scan(&got)
			return got, err
		},
	}
}

func newMySQLFTSStampBackend(t *testing.T) *ftsStampBackend {
	t.Helper()
	dsn := os.Getenv(conformanceDSNEnv)
	if dsn == "" {
		msg := conformanceDSNEnv + " not set — skipping MariaDB/MySQL FTS stamp leg"
		if os.Getenv(conformanceRequireEnv) == "1" {
			t.Fatalf("%s (%s=1, MySQL must not be skipped)", msg, conformanceRequireEnv)
		}
		t.Skip(msg)
	}
	pool, err := db.OpenMySQL(dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	truncateMySQL(t, pool)

	return &ftsStampBackend{
		name:   "mysql",
		notes:  mysql.NewNoteRepo(pool.WriteDB),
		search: mysql.NewSearchRepo(pool.WriteDB),
		users:  mysql.NewUserRepo(pool.WriteDB),
		setStamp: func(ctx context.Context, noteID int64) error {
			_, err := pool.WriteDB.ExecContext(ctx,
				`UPDATE notes SET fts_synced_at = UTC_TIMESTAMP() WHERE id = ?`, noteID)
			return err
		},
		rawStamp: func(ctx context.Context, noteID int64) (sql.NullString, error) {
			var got sql.NullString
			err := pool.ReadDB.QueryRowContext(ctx,
				`SELECT fts_synced_at FROM notes WHERE id = ?`, noteID).Scan(&got)
			return got, err
		},
	}
}

// markFresh puts the note in the state the sync path leaves: indexed, stamped.
// On SQLite that is a real search (which is what syncs a stale note); on MySQL
// the stamp is written directly because no production path writes one.
func (b *ftsStampBackend) markFresh(t *testing.T, userID, noteID int64, term string) {
	t.Helper()
	ctx := context.Background()
	if b.name == "sqlite" {
		_, err := b.search.Search(ctx, userID, term, nil)
		require.NoError(t, err, "%s: search to sync the note", b.name)
	} else {
		require.NoError(t, b.setStamp(ctx, noteID), "%s: stamp the note", b.name)
	}

	got, err := b.notes.GetByID(ctx, userID, noteID)
	require.NoError(t, err, "%s: read note", b.name)
	require.NotNil(t, got.FtsSyncedAt,
		"%s: the note must be fresh before the write under test — otherwise the assertion is vacuous",
		b.name)
}

// requireStale asserts the note carries no sync stamp, twice: through the
// repository's own read and through a raw read of the stored column.
func (b *ftsStampBackend) requireStale(t *testing.T, userID, noteID int64, step string) {
	t.Helper()
	ctx := context.Background()

	got, err := b.notes.GetByID(ctx, userID, noteID)
	require.NoError(t, err, "%s: read note after %s", b.name, step)
	assert.Nil(t, got.FtsSyncedAt,
		"%s: %s must clear fts_synced_at — the index entry describes text the note no longer has", b.name, step)

	raw, err := b.rawStamp(ctx, noteID)
	require.NoError(t, err, "%s: raw stamp read after %s", b.name, step)
	assert.False(t, raw.Valid,
		"%s: %s must store fts_synced_at as NULL, not a zero timestamp", b.name, step)
}

func TestNoteRepo_UpdatePathsClearFTSStamp_SQLite(t *testing.T) {
	runFTSStamp(t, newSQLiteFTSStampBackend(t))
}

func TestNoteRepo_UpdatePathsClearFTSStamp_MySQL(t *testing.T) {
	runFTSStamp(t, newMySQLFTSStampBackend(t))
}

func runFTSStamp(t *testing.T, b *ftsStampBackend) {
	t.Helper()
	ctx := context.Background()

	user, err := b.users.Create(ctx, "fts-stamp-user", "hash", "fts-stamp-uuid")
	require.NoError(t, err, "%s: create user", b.name)

	note, err := b.notes.Create(ctx, &model.Note{
		UserID: user.ID, Title: "Quokka Ledger", TitleKey: "quokka ledger",
		Slug: "quokka-ledger", DiskPath: "fts-stamp-uuid/quokka-ledger.md",
		Content: "the badger dug", ContentHash: "hash-1",
	})
	require.NoError(t, err, "%s: create note", b.name)

	// ── 1. a rename clears the stamp ─────────────────────────────────────────
	// The title is an indexed column, so a rename is an index change even when
	// the body does not move. If this write path leaves the stamp set, the next
	// search trusts an entry that still holds the old title's tokens.
	b.markFresh(t, user.ID, note.ID, "badger")

	renamed, err := b.notes.GetByID(ctx, user.ID, note.ID)
	require.NoError(t, err, "%s: read note before rename", b.name)
	renamed.Title = "Wombat Ledger"
	renamed.TitleKey = "wombat ledger"
	require.NoError(t, b.notes.Update(ctx, renamed), "%s: rename", b.name)
	b.requireStale(t, user.ID, note.ID, "a rename (Update)")

	// The column and the observable behaviour agree: the old title's word stops
	// matching, the new one starts.
	old, err := b.search.Search(ctx, user.ID, "quokka", nil)
	require.NoError(t, err, "%s: search old title", b.name)
	assert.Empty(t, old, "%s: the retired title must not match", b.name)
	fresh, err := b.search.Search(ctx, user.ID, "wombat", nil)
	require.NoError(t, err, "%s: search new title", b.name)
	require.Len(t, fresh, 1, "%s: the new title must match", b.name)

	// ── 2. a rename that also moves the body clears the stamp ────────────────
	b.markFresh(t, user.ID, note.ID, "wombat")

	moved, err := b.notes.GetByID(ctx, user.ID, note.ID)
	require.NoError(t, err, "%s: read note before retitle", b.name)
	moved.Title = "Numbat Ledger"
	moved.TitleKey = "numbat ledger"
	moved.Content = "the badger dug deeper"
	moved.ContentHash = "hash-2"
	require.NoError(t, b.notes.UpdateMetadataAndContent(ctx, moved), "%s: retitle with content", b.name)
	b.requireStale(t, user.ID, note.ID, "a rename that moves the body (UpdateMetadataAndContent)")

	// ── 3. a body edit clears the stamp ──────────────────────────────────────
	b.markFresh(t, user.ID, note.ID, "numbat")

	require.NoError(t, b.notes.UpdateContent(ctx, user.ID, note.ID,
		"the possum dug deeper", "hash-3", "hash-2"), "%s: edit content", b.name)
	b.requireStale(t, user.ID, note.ID, "a body edit (UpdateContent)")

	// And the note is still findable by what it now says: clearing the stamp is
	// only correct because something re-indexes it afterwards.
	found, err := b.search.Search(ctx, user.ID, "possum", nil)
	require.NoError(t, err, "%s: search edited body", b.name)
	require.Len(t, found, 1, "%s: the edited body must be searchable", b.name)

	if b.name == "sqlite" {
		// The re-index stamps the note. Asserted on SQLite only: it is the
		// backend with a sync step. MySQL's SyncNote is a documented no-op
		// (InnoDB maintains the index itself), so nothing there ever writes a
		// non-NULL stamp and asserting one would be inventing a contract.
		after, err := b.notes.GetByID(ctx, user.ID, note.ID)
		require.NoError(t, err, "%s: read note after re-sync", b.name)
		assert.NotNil(t, after.FtsSyncedAt,
			"%s: the search that re-indexed the note must re-stamp it", b.name)
	}
}
