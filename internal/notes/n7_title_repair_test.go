package notes_test

// N7: the row's title and the file's "# H1" could diverge and nothing repaired
// it. Reconcile compared content_hash only, so a heading edited on disk synced
// its content and left the title (and therefore title_key, the [[wikilink]]
// target and the indexed title) describing a heading the file no longer had.
//
// These tests state the repair rule on both backends:
//
//   - a file that states an H1 is authoritative for the title; the DB row
//     converges onto it, and the link graph follows in the same pass;
//   - a file that states no H1 has no opinion, so a title typed in the UI is
//     never overwritten by the filename;
//   - the filename is never rewritten (no rename, so no collision and no
//     case-only self-collision), and nothing on this path deletes a note;
//   - a converged title costs nothing on the next pass — the content_hash fast
//     path is untouched and no title is rewritten when it already matches.
//
// The MySQL leg runs whenever THORNOTES_TEST_MYSQL_DSN is set (as in CI) and
// skips otherwise; THORNOTES_REQUIRE_MYSQL=1 turns that skip into a failure.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/dbtest"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository/mysql"
	"github.com/th0rn0/thornotes/internal/wikilinks"
)

const (
	titleRepairDSNEnv     = "THORNOTES_TEST_MYSQL_DSN"
	titleRepairRequireEnv = "THORNOTES_REQUIRE_MYSQL"
)

// TestMain takes the shared-schema advisory lock whenever the MySQL DSN is set:
// this package truncates the same database the repository packages use, and
// `go test ./...` runs packages in parallel processes.
func TestMain(m *testing.M) {
	os.Exit(dbtest.Locked(titleRepairDSNEnv, m.Run))
}

// titleStack is the one service plus the two handles the tests need: the notes
// root on disk, and the write connection for seeding a divergence that the
// service itself would refuse to create.
type titleStack struct {
	name     string
	svc      *notes.Service
	user     *model.User
	userID   int64
	notesDir string
	writeDB  *sql.DB
}

func newSQLiteTitleStack(t *testing.T) *titleStack {
	t.Helper()
	st := newTestStackFull(t)
	return &titleStack{
		name:     "sqlite",
		svc:      st.svc,
		user:     st.user,
		userID:   st.userID,
		notesDir: st.notesDir,
		writeDB:  st.pool.WriteDB,
	}
}

func newMySQLTitleStack(t *testing.T) *titleStack {
	t.Helper()
	dsn := os.Getenv(titleRepairDSNEnv)
	if dsn == "" {
		msg := titleRepairDSNEnv + " not set — skipping MariaDB/MySQL title-repair leg"
		if os.Getenv(titleRepairRequireEnv) == "1" {
			t.Fatalf("%s (%s=1, MySQL must not be skipped)", msg, titleRepairRequireEnv)
		}
		t.Skip(msg)
	}
	pool, err := db.OpenMySQL(dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	truncateTitleRepairMySQL(t, pool)

	dir := t.TempDir()
	fsDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(fsDir)
	require.NoError(t, err)

	ctx := context.Background()
	user, err := mysql.NewUserRepo(pool.WriteDB).Create(
		ctx, "testuser", "$2a$12$fakehash0000000000000000000000000000000000000000000000", "test-uuid")
	require.NoError(t, err)

	svc := notes.NewService(
		mysql.NewNoteRepo(pool.WriteDB),
		mysql.NewFolderRepo(pool.WriteDB),
		mysql.NewSearchRepo(pool.WriteDB),
		mysql.NewJournalRepo(pool.WriteDB),
		mysql.NewLinkRepo(pool.WriteDB),
		fs,
	)
	return &titleStack{
		name:     "mysql",
		svc:      svc,
		user:     user,
		userID:   user.ID,
		notesDir: fsDir,
		writeDB:  pool.WriteDB,
	}
}

// truncateTitleRepairMySQL empties every application table so each subtest
// starts from a known-empty schema (the repository packages' helper, which this
// package cannot import).
func truncateTitleRepairMySQL(t *testing.T, pool *db.Pool) {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.WriteDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0")
	require.NoError(t, err)
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS = 1")
	}()
	for _, table := range []string{
		"api_token_folder_permissions", "api_tokens", "journals", "note_links",
		"notes", "sessions", "folders", "users",
	} {
		_, err := conn.ExecContext(ctx, "DELETE FROM "+table)
		require.NoError(t, err, "reset table %s", table)
	}
}

// writeNoteFile replaces a note's file on disk, the way an external editor
// would. It asserts the write so a failing fixture cannot look like a passing
// assertion about the row.
func writeNoteFile(t *testing.T, st *titleStack, diskPath, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(st.notesDir, diskPath), []byte(content), 0600))
}

func readNoteFile(t *testing.T, st *titleStack, diskPath string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(st.notesDir, diskPath))
	require.NoError(t, err)
	return string(b)
}

type titleRepairCase struct {
	name string
	run  func(t *testing.T, st *titleStack)
}

func titleRepairBackends() []struct {
	name     string
	newStack func(*testing.T) *titleStack
} {
	return []struct {
		name     string
		newStack func(*testing.T) *titleStack
	}{
		{"sqlite", newSQLiteTitleStack},
		{"mysql", newMySQLTitleStack},
	}
}

func titleRepairCases() []titleRepairCase {
	return []titleRepairCase{
		{"heading edit on disk converges the title", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Original Title", nil)
			require.NoError(t, err)

			writeNoteFile(t, st, n.DiskPath, "# Renamed On Disk\n\nbody text")

			changed, err := st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)
			assert.Greater(t, changed, 0, "the pass must report the change it made")

			got, err := st.svc.GetNote(ctx, st.userID, n.ID)
			require.NoError(t, err, "the note row must survive the repair")
			assert.Equal(t, "Renamed On Disk", got.Title)
			assert.Equal(t, wikilinks.NormalizeKey("Renamed On Disk"), got.TitleKey)
			assert.Equal(t, n.Slug, got.Slug, "the filename stem is not rederived from the title")
			assert.Equal(t, n.DiskPath, got.DiskPath, "no file is renamed by a repair pass")
			assert.Equal(t, "# Renamed On Disk\n\nbody text", readNoteFile(t, st, n.DiskPath),
				"the file the user wrote is left exactly as they wrote it")
		}},

		{"a converged title is not rewritten on the next pass", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Before", nil)
			require.NoError(t, err)
			writeNoteFile(t, st, n.DiskPath, "# After\n")

			_, err = st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)
			first, err := st.svc.GetNote(ctx, st.userID, n.ID)
			require.NoError(t, err)
			require.Equal(t, "After", first.Title)

			changed, err := st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)
			assert.Zero(t, changed, "a steady-state pass must write nothing (content_hash fast path intact)")

			second, err := st.svc.GetNote(ctx, st.userID, n.ID)
			require.NoError(t, err)
			assert.Equal(t, first.Title, second.Title)
			assert.Equal(t, first.UpdatedAt.UTC(), second.UpdatedAt.UTC(), "no write means updated_at must not move")
		}},

		{"a case-only heading change converges", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Foo Bar", nil)
			require.NoError(t, err)
			writeNoteFile(t, st, n.DiskPath, "# foo bar\n")

			_, err = st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)

			got, err := st.svc.GetNote(ctx, st.userID, n.ID)
			require.NoError(t, err)
			assert.Equal(t, "foo bar", got.Title, "a case-only correction is a real title change")
			assert.Equal(t, n.DiskPath, got.DiskPath, "and it is not a rename")
			require.FileExists(t, filepath.Join(st.notesDir, n.DiskPath))
		}},

		{"a case-only title change on the write path is never a rename", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Foo Bar", nil)
			require.NoError(t, err)

			// slugify lowercases, so a case-only title change produces the same
			// slug and the rename branch is never entered. That is why the write
			// path cannot hand renameat2 a source and destination that differ
			// only by case and get "collides with itself" back.
			require.NoError(t, st.svc.UpdateNoteMetadata(ctx, st.userID, st.user.UUID, n.ID, "foo bar", nil))

			got, err := st.svc.GetNote(ctx, st.userID, n.ID)
			require.NoError(t, err)
			assert.Equal(t, "foo bar", got.Title)
			assert.Equal(t, n.Slug, got.Slug)
			assert.Equal(t, n.DiskPath, got.DiskPath)
			require.FileExists(t, filepath.Join(st.notesDir, n.DiskPath))
		}},

		{"search finds the note by the title the file states", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Original Title", nil)
			require.NoError(t, err)
			writeNoteFile(t, st, n.DiskPath, "# Renamed On Disk\n\nbody text")

			_, err = st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)

			// The title is an indexed column, so a converged title must also mark
			// the note stale for the index — otherwise search keeps answering with
			// the title the file no longer states.
			got, err := st.svc.Search(ctx, st.userID, "Renamed", nil)
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, n.ID, got[0].NoteID)
			assert.Equal(t, "Renamed On Disk", got[0].Title)

			stale, err := st.svc.Search(ctx, st.userID, "Original", nil)
			require.NoError(t, err)
			assert.Empty(t, stale, "the old title's tokens must not answer a search any more")
		}},

		{"a title that matches another note's is not an error and deletes nothing", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			alpha, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Alpha", nil)
			require.NoError(t, err)
			beta, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Beta", nil)
			require.NoError(t, err)

			// Two files may state the same heading; the disk allows it, so the
			// index must too. Nothing renames, so there is no file to collide
			// with — the only overlap is a shared title_key.
			writeNoteFile(t, st, alpha.DiskPath, "# Shared Heading\n\nalpha body")
			writeNoteFile(t, st, beta.DiskPath, "# Shared Heading\n\nbeta body")

			_, err = st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)

			for _, id := range []int64{alpha.ID, beta.ID} {
				got, err := st.svc.GetNote(ctx, st.userID, id)
				require.NoError(t, err, "note %d must survive a colliding title", id)
				assert.Equal(t, "Shared Heading", got.Title)
			}
			assert.Equal(t, "# Shared Heading\n\nalpha body", readNoteFile(t, st, alpha.DiskPath))
			assert.Equal(t, "# Shared Heading\n\nbeta body", readNoteFile(t, st, beta.DiskPath))

			all, err := st.svc.ListAllNotes(ctx, st.userID)
			require.NoError(t, err)
			assert.Len(t, all, 2)
		}},

		{"a note with no heading keeps the title the user gave it", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Human Title", nil)
			require.NoError(t, err)

			changed, err := st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)
			assert.Zero(t, changed, "an empty body states no title and must not cause a write")

			got, err := st.svc.GetNote(ctx, st.userID, n.ID)
			require.NoError(t, err)
			assert.Equal(t, "Human Title", got.Title, "the filename must not overwrite a human title")
		}},

		{"removing the heading keeps the last stated title", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Draft", nil)
			require.NoError(t, err)
			_, err = st.svc.UpdateNoteContent(ctx, st.userID, n.ID, "# Stated Title\n\nbody", n.ContentHash)
			require.NoError(t, err)

			_, err = st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)
			stated, err := st.svc.GetNote(ctx, st.userID, n.ID)
			require.NoError(t, err)
			require.Equal(t, "Stated Title", stated.Title)

			writeNoteFile(t, st, n.DiskPath, "body only, no heading any more")
			_, err = st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)

			got, err := st.svc.GetNote(ctx, st.userID, n.ID)
			require.NoError(t, err)
			assert.Equal(t, "Stated Title", got.Title, "a file with no H1 has no opinion about the title")
			assert.Equal(t, "body only, no heading any more", got.Content, "content still converges")
		}},

		{"the link graph follows the converged title in the same pass", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			target, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Placeholder", nil)
			require.NoError(t, err)
			source, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Source Page", nil)
			require.NoError(t, err)
			_, err = st.svc.UpdateNoteContent(ctx, st.userID, source.ID,
				"see [[Renamed On Disk]]", source.ContentHash)
			require.NoError(t, err)

			// Before: nobody states that heading, so the link is unresolved.
			_, err = st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)
			before, err := st.svc.OutgoingLinks(ctx, st.userID, source.ID)
			require.NoError(t, err)
			require.Len(t, before, 1)
			require.Nil(t, before[0].ToNoteID, "no note states that heading yet")

			// An external editor changes the H1; the pass that converges the
			// row's title must re-resolve the edge in the same pass, or the graph
			// inherits a title the file no longer contains.
			writeNoteFile(t, st, target.DiskPath, "# Renamed On Disk\n\ntarget body")
			_, err = st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)

			after, err := st.svc.OutgoingLinks(ctx, st.userID, source.ID)
			require.NoError(t, err)
			require.Len(t, after, 1)
			require.NotNil(t, after[0].ToNoteID)
			assert.Equal(t, target.ID, *after[0].ToNoteID,
				"the heading the file states resolves to the note that states it")
			assert.NotNil(t, after[0].ResolvedAt)
		}},

		{"an existing divergence repairs even when the content hash already matches", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Stale Title", nil)
			require.NoError(t, err)

			// The state a pre-fix reconcile left behind: content and hash agree
			// with the disk, the title does not. Seeded directly because no
			// service path can produce it any more.
			const content = "# Real Title\n\nbody"
			writeNoteFile(t, st, n.DiskPath, content)
			_, err = st.writeDB.ExecContext(ctx,
				`UPDATE notes SET content = ?, content_hash = ?, title = ?, title_key = ? WHERE id = ?`,
				content, notes.HashContent(content), "Stale Title", wikilinks.NormalizeKey("Stale Title"), n.ID)
			require.NoError(t, err)

			_, err = st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)

			got, err := st.svc.GetNote(ctx, st.userID, n.ID)
			require.NoError(t, err)
			assert.Equal(t, "Real Title", got.Title,
				"title convergence must not be gated on the content hash having moved")
			assert.Equal(t, notes.HashContent(content), got.ContentHash, "the content write is not repeated")
		}},
	}
}

func TestN7_TitleRepair(t *testing.T) {
	for _, backend := range titleRepairBackends() {
		t.Run(backend.name, func(t *testing.T) {
			for _, tc := range titleRepairCases() {
				t.Run(tc.name, func(t *testing.T) {
					tc.run(t, backend.newStack(t))
				})
			}
		})
	}
}

// TestN7_ReconcileNeverDeletesOnTitleRepair is the guard the brief asked for:
// a converging repair must have no path that removes a note. It exercises the
// repair and then asserts every row and every file is still there.
func TestN7_ReconcileNeverDeletesOnTitleRepair(t *testing.T) {
	for _, backend := range titleRepairBackends() {
		t.Run(backend.name, func(t *testing.T) {
			st := backend.newStack(t)
			ctx := context.Background()

			first, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "One", nil)
			require.NoError(t, err)
			second, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Two", nil)
			require.NoError(t, err)

			writeNoteFile(t, st, first.DiskPath, "# Retitled One\n")
			writeNoteFile(t, st, second.DiskPath, "# Retitled Two\n")

			_, err = st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)

			all, err := st.svc.ListAllNotes(ctx, st.userID)
			require.NoError(t, err)
			assert.Len(t, all, 2, "a title repair deletes nothing")
			for _, n := range []*model.Note{first, second} {
				require.FileExists(t, filepath.Join(st.notesDir, n.DiskPath))
			}
		})
	}
}
