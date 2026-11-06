package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// These tests cover the difference between a row that *matched* an UPDATE and a
// row that *changed* (ticket 599). MySQL and MariaDB report the second, not the
// first, in RowsAffected(): a single-row UPDATE that rewrites the values the row
// already holds reports 0, exactly like an UPDATE whose WHERE clause matched
// nothing. Reading that 0 as "no such row" turned every same-value write into a
// false 404 (note metadata save, folder rename, share-token clear) or a false
// 409 (content save), and only on MySQL — SQLite counts matched rows, so the
// rest of the suite stays green on the backend that does not have the bug.

// pinnedUnix is the fixed instant the unchanged-value tests pin the database
// session clock to: 2023-11-14 22:13:20 UTC. The value is arbitrary; what
// matters is that it cannot move while the test runs.
const pinnedUnix int64 = 1700000000

// pinnedStamp is pinnedUnix rendered the way DATE_FORMAT returns a DATETIME.
var pinnedStamp = time.Unix(pinnedUnix, 0).UTC().Format("2006-01-02 15:04:05")

// openPinnedClockMySQL opens a *sql.DB for the shared test database whose single
// session has @@timestamp pinned to unix, so NOW()/UTC_TIMESTAMP() — and
// therefore every updated_at these repositories write — is constant for the
// whole test.
//
// The unchanged-value cases need that pin to be genuine no-ops. On a real clock
// "save the same value" is a no-op only if the second does not tick between the
// setup statement and the call under test: if it does, the UPDATE changes
// updated_at, the row counts as changed, and the test passes without ever
// exercising the zero-changed-rows branch it exists to cover. That is a race
// that would hide the bug on a slow or unlucky run, so the clock is pinned
// instead of hoped for.
//
// SET timestamp is a *session* variable, hence the single-connection pool: a
// second connection would be created on the real clock and would silently turn
// the no-op write back into a changed row.
func openPinnedClockMySQL(t *testing.T, unix int64) *sql.DB {
	t.Helper()

	dsn := os.Getenv(mysqlDSNEnv)
	require.NotEmpty(t, dsn, mysqlDSNEnv+" not set")

	conn, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	conn.SetConnMaxLifetime(0)
	conn.SetConnMaxIdleTime(0)

	_, err = conn.ExecContext(context.Background(), fmt.Sprintf("SET timestamp = %d", unix))
	require.NoError(t, err)

	return conn
}

// pinNoteUpdatedAt stamps the note's updated_at with the session's (pinned) now
// and returns the stored value, so a test can assert that the write it is about
// to make really is a no-op rather than one second away from one.
func pinNoteUpdatedAt(t *testing.T, conn *sql.DB, noteID int64) string {
	t.Helper()
	ctx := context.Background()

	_, err := conn.ExecContext(ctx,
		`UPDATE notes SET updated_at = UTC_TIMESTAMP() WHERE id = ?`, noteID)
	require.NoError(t, err)

	var stamp string
	require.NoError(t, conn.QueryRowContext(ctx,
		`SELECT DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s') FROM notes WHERE id = ?`, noteID,
	).Scan(&stamp))
	return stamp
}

// createMySQLSecondUser makes a second account, for the not-yours cases.
// createMySQLUser always claims "user1", so it cannot be called twice.
func createMySQLSecondUser(t *testing.T, dsnConn *sql.DB) *model.User {
	t.Helper()
	user, err := NewUserRepo(dsnConn).Create(context.Background(), "user2", "hash", "test-uuid-2")
	require.NoError(t, err)
	return user
}

func TestMySQL_NoteRepo_Update_UnchangedValues_IsSuccess(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	db := openPinnedClockMySQL(t, pinnedUnix)
	repo := NewNoteRepo(db)

	created, err := repo.Create(context.Background(), &model.Note{
		UserID:      user.ID,
		Title:       "Unchanged MySQL Title",
		Slug:        "unchanged-mysql-title",
		TitleKey:    "unchanged mysql title",
		DiskPath:    "uuid-1/unchanged-mysql-title.md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{"mysql"},
	})
	require.NoError(t, err)

	require.Equal(t, pinnedStamp, pinNoteUpdatedAt(t, db, created.ID),
		"precondition: the session clock must be pinned, or the update below is not a no-op")

	// Re-save exactly the values the row already holds — a PUT that repeats the
	// current title and tags. No column changes, so RowsAffected() is 0, the
	// same count a nonexistent note produces. The row matched, so this is
	// success; answering 404 is the bug this test exists for.
	err = repo.Update(context.Background(), created)
	require.NoError(t, err, "a same-value save matched a row: it is success, not 404")

	got, err := repo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "Unchanged MySQL Title", got.Title)
	assert.Equal(t, []string{"mysql"}, got.Tags)
}

func TestMySQL_NoteRepo_Update_OtherUsersNote_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	other := createMySQLSecondUser(t, pool.WriteDB)
	repo := NewNoteRepo(pool.WriteDB)

	created := createMySQLNote(t, pool, user.ID, nil, "Owned By User One", "owned-by-user-one")

	// A same-value write cannot happen here — "Stolen" differs from the stored
	// title — so this case is unchanged by ticket 599: a row that is not the
	// caller's must stay a 404, and must not be written.
	stolen := *created
	stolen.Title = "Stolen"
	err := repo.Update(context.Background(), &model.Note{
		ID: created.ID, UserID: other.ID, Title: stolen.Title,
		Slug: created.Slug, TitleKey: created.TitleKey, DiskPath: created.DiskPath,
		Tags: created.Tags,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)

	got, err := repo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "Owned By User One", got.Title, "a foreign user's update must not write the row")
}

func TestMySQL_NoteRepo_UpdateContent_UnchangedContent_IsSuccess(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	db := openPinnedClockMySQL(t, pinnedUnix)
	repo := NewNoteRepo(db)

	const (
		body = "unchanged body"
		hash = "unchanged-content-hash"
	)

	created, err := repo.Create(context.Background(), &model.Note{
		UserID:      user.ID,
		Title:       "Unchanged Content MySQL Note",
		Slug:        "unchanged-content-mysql-note",
		TitleKey:    "unchanged content mysql note",
		DiskPath:    "uuid-1/unchanged-content-mysql-note.md",
		Content:     body,
		ContentHash: hash,
		Tags:        []string{},
	})
	require.NoError(t, err)

	require.Equal(t, pinnedStamp, pinNoteUpdatedAt(t, db, created.ID),
		"precondition: the session clock must be pinned, or the update below is not a no-op")

	// Saving identical content with the hash the row already has: the CAS
	// predicate matches, every written value is already there. Zero changed rows,
	// zero mismatched hash — success, not the 409 an optimistic-concurrency
	// conflict returns.
	err = repo.UpdateContent(context.Background(), user.ID, created.ID, body, hash, hash)
	require.NoError(t, err, "identical content with a matching expected hash is a no-op write, not a conflict")

	got, err := repo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, body, got.Content)
	assert.Equal(t, hash, got.ContentHash)
}

func TestMySQL_NoteRepo_UpdateContent_OtherUsersNote_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	other := createMySQLSecondUser(t, pool.WriteDB)
	repo := NewNoteRepo(pool.WriteDB)

	created := createMySQLNote(t, pool, user.ID, nil, "Content Owned By User One", "content-owned-by-user-one")

	// Not the caller's note: a 404, never the 409 a stale-hash conflict produces
	// (and never a hint that the id exists).
	err := repo.UpdateContent(context.Background(), other.ID, created.ID,
		"stolen content", "stolen-hash", created.ContentHash)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
	assert.NotErrorIs(t, err, apperror.ErrConflict)
}

func TestMySQL_FolderRepo_Rename_ToCurrentName_IsSuccess(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderRepo(pool.WriteDB)

	folder, err := repo.Create(context.Background(), user.ID, nil, "Same-Name-MySQL", "uuid-1/Same-Name-MySQL")
	require.NoError(t, err)

	// Rename to the name and path the folder already has. The UPDATE writes no
	// new value, so MySQL reports 0 rows affected; the folder does exist and is
	// the caller's, so the answer is success, not 404. folders carries no
	// updated_at, so this case is a genuine no-op without a pinned clock.
	err = repo.Rename(context.Background(), user.ID, folder.ID, "Same-Name-MySQL", "uuid-1/Same-Name-MySQL")
	require.NoError(t, err, "a same-name rename matched a row: it is success, not 404")

	got, err := repo.GetByID(context.Background(), user.ID, folder.ID)
	require.NoError(t, err)
	assert.Equal(t, "Same-Name-MySQL", got.Name)
	assert.Equal(t, "uuid-1/Same-Name-MySQL", got.DiskPath)
}

func TestMySQL_FolderRepo_Rename_OtherUsersFolder_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	other := createMySQLSecondUser(t, pool.WriteDB)
	repo := NewFolderRepo(pool.WriteDB)

	folder, err := repo.Create(context.Background(), user.ID, nil, "Owned-MySQL", "uuid-1/Owned-MySQL")
	require.NoError(t, err)

	err = repo.Rename(context.Background(), other.ID, folder.ID, "Stolen-MySQL", "uuid-1/Stolen-MySQL")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)

	got, err := repo.GetByID(context.Background(), user.ID, folder.ID)
	require.NoError(t, err)
	assert.Equal(t, "Owned-MySQL", got.Name, "a foreign user's rename must not write the row")
}
