package mysql

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/db"
)

// These tests cover the link-re-resolution write on the driver that reports
// *changed* rows (ticket 600). UpdateResolution used to discard its Result
// entirely, so an UPDATE that matched no row — an edge that was never the
// caller's, or one a concurrent full reindex had already replaced — reported
// success, and the caller went on as if the link had been re-pointed while the
// stored row still held the old target. The same write also has to survive the
// opposite mistake: re-marking an edge unresolved when it already is writes no
// new value, and must not be read as "no such row".

// createMySQLLink inserts a note_links row for fromNoteID and returns its id.
// toNoteID nil means "not resolved"; the row's resolved_at is NULL in that case.
func createMySQLLink(t *testing.T, pool *db.Pool, userID, fromNoteID int64, toNoteID *int64, rawTarget, targetHash string) int64 {
	t.Helper()
	res, err := pool.WriteDB.ExecContext(context.Background(),
		`INSERT INTO note_links (user_id, from_note_id, to_note_id, raw_target, target_hash)
		 VALUES (?, ?, ?, ?, ?)`,
		userID, fromNoteID, toNoteID, rawTarget, targetHash,
	)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

func readMySQLLinkResolution(t *testing.T, pool *db.Pool, linkID int64) (sql.NullInt64, int64, sql.NullTime) {
	t.Helper()
	var (
		toNoteID   sql.NullInt64
		ambiguous  int64
		resolvedAt sql.NullTime
	)
	require.NoError(t, pool.WriteDB.QueryRowContext(context.Background(),
		`SELECT to_note_id, ambiguous, resolved_at FROM note_links WHERE id = ?`, linkID,
	).Scan(&toNoteID, &ambiguous, &resolvedAt))
	return toNoteID, ambiguous, resolvedAt
}

// The positive control: re-resolving an unresolved edge to a real target writes
// the new to_note_id and stamps resolved_at.
func TestMySQL_LinkRepo_UpdateResolution_ResolvesEdge(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewLinkRepo(pool.WriteDB)
	ctx := context.Background()

	from := createMySQLNote(t, pool, user.ID, nil, "Link Source MySQL", "link-source-mysql")
	target := createMySQLNote(t, pool, user.ID, nil, "Link Target MySQL", "link-target-mysql")
	linkID := createMySQLLink(t, pool, user.ID, from.ID, nil, "Link Target MySQL", "hash-resolve-mysql")

	require.NoError(t, repo.UpdateResolution(ctx, user.ID, linkID, &target.ID, false),
		"re-resolving an edge the caller owns must succeed")

	toNoteID, ambiguous, resolvedAt := readMySQLLinkResolution(t, pool, linkID)
	require.True(t, toNoteID.Valid)
	assert.Equal(t, target.ID, toNoteID.Int64)
	assert.Equal(t, int64(0), ambiguous)
	assert.True(t, resolvedAt.Valid, "a resolved edge carries resolved_at")
}

// The matched-but-unchanged case, and the reason this write needs updateScoped:
// the edge is already unresolved (to_note_id NULL, ambiguous 0, resolved_at
// NULL) and the rename pass re-marks it unresolved. MySQL changes no column and
// reports 0 rows affected — the same count a missing row produces. The row
// matched, so this is success; answering 404 is the bug this test exists for.
func TestMySQL_LinkRepo_UpdateResolution_UnchangedResolution_IsSuccess(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewLinkRepo(pool.WriteDB)
	ctx := context.Background()

	from := createMySQLNote(t, pool, user.ID, nil, "Unresolved Source MySQL", "unresolved-source-mysql")
	linkID := createMySQLLink(t, pool, user.ID, from.ID, nil, "Missing Page", "hash-unresolved-mysql")

	require.NoError(t, repo.UpdateResolution(ctx, user.ID, linkID, nil, false),
		"a same-value resolution write matched a row: it is success, not 404")

	toNoteID, _, resolvedAt := readMySQLLinkResolution(t, pool, linkID)
	assert.False(t, toNoteID.Valid)
	assert.False(t, resolvedAt.Valid)
}

func TestMySQL_LinkRepo_UpdateResolution_OtherUsersLink_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	other := createMySQLSecondUser(t, pool.WriteDB)
	repo := NewLinkRepo(pool.WriteDB)
	ctx := context.Background()

	from := createMySQLNote(t, pool, user.ID, nil, "Foreign Source MySQL", "foreign-source-mysql")
	target := createMySQLNote(t, pool, user.ID, nil, "Foreign Target MySQL", "foreign-target-mysql")
	linkID := createMySQLLink(t, pool, user.ID, from.ID, nil, "Foreign Target MySQL", "hash-foreign-mysql")

	err := repo.UpdateResolution(ctx, other.ID, linkID, &target.ID, false)
	require.Error(t, err, "an edge that is not the caller's must not report success")
	assert.ErrorIs(t, err, apperror.ErrNotFound)

	toNoteID, _, resolvedAt := readMySQLLinkResolution(t, pool, linkID)
	assert.False(t, toNoteID.Valid, "a foreign caller must not write the row")
	assert.False(t, resolvedAt.Valid)
}

func TestMySQL_LinkRepo_UpdateResolution_MissingLink_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewLinkRepo(pool.WriteDB)

	err := repo.UpdateResolution(context.Background(), user.ID, 99999999, nil, false)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}
