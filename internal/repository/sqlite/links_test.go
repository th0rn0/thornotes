package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
)

// These tests cover the link-re-resolution write (ticket 600). UpdateResolution
// used to discard its Result entirely, so an UPDATE that matched no row — an
// edge that was never the caller's, or one a concurrent full reindex had
// already replaced — reported success. The caller (notes.repairInbound) then
// went on as if the link had been re-pointed while the stored row still held
// the old target.

// createSecondUser makes a second account for the not-yours cases; createUser
// always claims "user1", so it cannot be called twice.
func createSecondUser(t *testing.T, pool *db.Pool) *model.User {
	t.Helper()
	user, err := NewUserRepo(pool.WriteDB).Create(context.Background(), "user2", "hash", "test-uuid-2")
	require.NoError(t, err)
	return user
}

// createLinkTestNote creates a note with a caller-chosen slug. createTestNote
// reuses one slug and one disk_path, so it cannot be called twice in a test.
func createLinkTestNote(t *testing.T, pool *db.Pool, userID int64, title, slug string) *model.Note {
	t.Helper()
	created, err := NewNoteRepo(pool.ReadDB, pool.WriteDB).Create(context.Background(), &model.Note{
		UserID:      userID,
		Title:       title,
		Slug:        slug,
		TitleKey:    slug,
		DiskPath:    "uuid-1/" + slug + ".md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	})
	require.NoError(t, err)
	return created
}

// createTestLink inserts a note_links row for fromNoteID and returns its id.
// toNoteID nil means "not resolved"; the row's resolved_at is NULL in that case.
func createTestLink(t *testing.T, pool *db.Pool, userID, fromNoteID int64, toNoteID *int64, rawTarget, targetHash string) int64 {
	t.Helper()
	var id int64
	err := pool.WriteDB.QueryRowContext(context.Background(),
		`INSERT INTO note_links (user_id, from_note_id, to_note_id, raw_target, target_hash)
		 VALUES (?, ?, ?, ?, ?) RETURNING id`,
		userID, fromNoteID, toNoteID, rawTarget, targetHash,
	).Scan(&id)
	require.NoError(t, err)
	return id
}

func readLinkResolution(t *testing.T, pool *db.Pool, linkID int64) (sql.NullInt64, int64, sql.NullTime) {
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
func TestLinkRepo_UpdateResolution_ResolvesEdge(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	linkRepo := NewLinkRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	from := createLinkTestNote(t, pool, user.ID, "Link Source", "link-source")
	target := createLinkTestNote(t, pool, user.ID, "Link Target", "link-target")
	linkID := createTestLink(t, pool, user.ID, from.ID, nil, "Link Target", "hash-resolve")

	require.NoError(t, linkRepo.UpdateResolution(ctx, user.ID, linkID, &target.ID, false),
		"re-resolving an edge the caller owns must succeed")

	toNoteID, ambiguous, resolvedAt := readLinkResolution(t, pool, linkID)
	require.True(t, toNoteID.Valid)
	assert.Equal(t, target.ID, toNoteID.Int64)
	assert.Equal(t, int64(0), ambiguous)
	assert.True(t, resolvedAt.Valid, "a resolved edge carries resolved_at")
}

// A write that changes nothing must still succeed: SQLite reports matched rows,
// so the zero-changed-rows branch is only reachable on MySQL, but a future
// "n == 0 means not-found" shortcut here would break exactly this case.
func TestLinkRepo_UpdateResolution_SameValue_IsSuccess(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	linkRepo := NewLinkRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	from := createLinkTestNote(t, pool, user.ID, "Unresolved Source", "unresolved-source")
	linkID := createTestLink(t, pool, user.ID, from.ID, nil, "Missing Page", "hash-unresolved")

	require.NoError(t, linkRepo.UpdateResolution(ctx, user.ID, linkID, nil, false),
		"re-marking an already-unresolved edge unresolved is a no-op write, not a 404")
}

func TestLinkRepo_UpdateResolution_OtherUsersLink_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	other := createSecondUser(t, pool)
	linkRepo := NewLinkRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	from := createLinkTestNote(t, pool, user.ID, "Foreign Source", "foreign-source")
	target := createLinkTestNote(t, pool, user.ID, "Foreign Target", "foreign-target")
	linkID := createTestLink(t, pool, user.ID, from.ID, nil, "Foreign Target", "hash-foreign")

	err := linkRepo.UpdateResolution(ctx, other.ID, linkID, &target.ID, false)
	require.Error(t, err, "an edge that is not the caller's must not report success")
	assert.ErrorIs(t, err, apperror.ErrNotFound)

	toNoteID, _, resolvedAt := readLinkResolution(t, pool, linkID)
	assert.False(t, toNoteID.Valid, "a foreign caller must not write the row")
	assert.False(t, resolvedAt.Valid)
}

func TestLinkRepo_UpdateResolution_MissingLink_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	linkRepo := NewLinkRepo(pool.ReadDB, pool.WriteDB)

	err := linkRepo.UpdateResolution(context.Background(), user.ID, 99999999, nil, false)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}
