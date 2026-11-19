package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
)

// FolderShareRepo.ListShares is the folder half of GET /api/v1/shares (ticket
// 616, slice 3). These are the repository-level facts the handler test cannot
// state: the row is shaped as a folder row, the name comes from the folders
// join, the token is redacted by the same function the note half uses, and
// another account's rows are excluded by SQL rather than by the caller.
//
// The two halves of the endpoint are asserted together in
// internal/handler/share_list_kinds_test.go; this file is what that test would
// still pass without, if the join or the scan were wrong in a way SQLite
// tolerates.

// folderShareFixture is one user with one folder, and a second user to prove the
// scoping with.
type folderShareFixture struct {
	pool    *db.Pool
	repo    *FolderShareRepo
	user    *model.User
	other   *model.User
	folder  *model.Folder
	folders *FolderRepo
}

func newFolderShareFixture(t *testing.T) *folderShareFixture {
	t.Helper()
	pool := openTestDB(t)
	user := createUser(t, pool)
	other, err := NewUserRepo(pool.WriteDB).Create(context.Background(), "user2", "hash", "test-uuid-2")
	require.NoError(t, err)

	folders := NewFolderRepo(pool.ReadDB, pool.WriteDB)
	folder, err := folders.Create(context.Background(), user.ID, nil, "work", "uuid/work")
	require.NoError(t, err)

	return &folderShareFixture{
		pool:    pool,
		repo:    NewFolderShareRepo(pool.ReadDB, pool.WriteDB),
		user:    user,
		other:   other,
		folder:  folder,
		folders: folders,
	}
}

func TestFolderShareRepo_ListShares_FolderRowShape(t *testing.T) {
	f := newFolderShareFixture(t)
	ctx := context.Background()

	token := strings.Repeat("ab", 16) // 32 hex characters, as the service mints them
	require.NoError(t, f.repo.UpsertToken(ctx, f.user.ID, f.folder.ID, token))

	shares, err := f.repo.ListShares(ctx, f.user.ID)
	require.NoError(t, err)
	require.Len(t, shares, 1)

	row := shares[0]
	assert.Equal(t, model.ShareKindFolder, row.Kind)
	assert.Nil(t, row.NoteID, "a folder link has no note id at all")
	require.NotNil(t, row.FolderID)
	assert.Equal(t, f.folder.ID, *row.FolderID)
	assert.Equal(t, "work", row.Title, "the folder's name is the title this list displays")
	assert.Equal(t, token[:8]+"…", row.TokenPrefix, "the same redaction the note half uses")
	assert.NotContains(t, row.TokenPrefix, token[8:], "the body of the token must not leak")
	assert.Nil(t, row.ExpiresAt, "no deadline is null, not a zero time")
	assert.Nil(t, row.LastViewedAt)
	assert.False(t, row.UpdatedAt.IsZero(), "a folder row's updated_at is its share row's created_at")

	// Scoped in SQL: another account's answer is empty, not filtered afterwards.
	otherShares, err := f.repo.ListShares(ctx, f.other.ID)
	require.NoError(t, err)
	assert.Empty(t, otherShares)
}

// TestFolderShareRepo_ListShares_ExpiredLinkIsListed pins the rule the note half
// already had: a dead link stays in the list with its deadline, so the owner can
// see why a URL they still have stopped answering. Filtering here would make the
// two halves of one endpoint disagree.
func TestFolderShareRepo_ListShares_ExpiredLinkIsListed(t *testing.T) {
	f := newFolderShareFixture(t)
	ctx := context.Background()

	require.NoError(t, f.repo.UpsertToken(ctx, f.user.ID, f.folder.ID, strings.Repeat("cd", 16)))
	past := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	_, err := f.pool.WriteDB.Exec(`UPDATE folder_shares SET expires_at = ? WHERE folder_id = ?`, past, f.folder.ID)
	require.NoError(t, err)

	shares, err := f.repo.ListShares(ctx, f.user.ID)
	require.NoError(t, err)
	require.Len(t, shares, 1, "an expired link is still a link that was handed out")
	require.NotNil(t, shares[0].ExpiresAt)
	assert.True(t, shares[0].ExpiresAt.Equal(past), "the deadline reads back as the instant it was set to")
}

// TestFolderShareRepo_ListShares_RevokedLinkIsGone: revoking is a DELETE, so a
// revoked link is not a filtered row, it is an absent one.
func TestFolderShareRepo_ListShares_RevokedLinkIsGone(t *testing.T) {
	f := newFolderShareFixture(t)
	ctx := context.Background()

	require.NoError(t, f.repo.UpsertToken(ctx, f.user.ID, f.folder.ID, strings.Repeat("ef", 16)))
	require.NoError(t, f.repo.Clear(ctx, f.user.ID, f.folder.ID))

	shares, err := f.repo.ListShares(ctx, f.user.ID)
	require.NoError(t, err)
	assert.Empty(t, shares)
}

// FolderShareRepo's deadline write, against the real SQLite statements.
//
// The SQL is the assertion here, not a status code: the guard is a predicate
// comparing a bound time against the column, and the interesting failure is a
// write that lands on a link that has already expired.

// folderShareLink creates a folder and installs a share token on it.
func folderShareLink(t *testing.T, pool *db.Pool, userID int64, name, token string) *model.Folder {
	t.Helper()
	ctx := context.Background()
	folder, err := NewFolderRepo(pool.ReadDB, pool.WriteDB).Create(ctx, userID, nil, name, "1/"+name)
	require.NoError(t, err)

	shares := NewFolderShareRepo(pool.ReadDB, pool.WriteDB)
	require.NoError(t, shares.UpsertToken(ctx, userID, folder.ID, token))
	return folder
}

// folderDeadline reads the deadline as the row holds it. A refused write is
// proved by this value being unchanged, not by the error alone.
func folderDeadline(t *testing.T, pool *db.Pool, folderID int64) *time.Time {
	t.Helper()
	var expiresAt *time.Time
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT expires_at FROM folder_shares WHERE folder_id = ?`, folderID).Scan(&expiresAt))
	return expiresAt
}

func folderToken(t *testing.T, pool *db.Pool, folderID int64) string {
	t.Helper()
	var token string
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT token FROM folder_shares WHERE folder_id = ?`, folderID).Scan(&token))
	return token
}

// TestFolderShareRepo_SetExpiry_LiveLinkStaysEditable is the half PATCH exists
// for: a live link's deadline can be added, extended and cleared, the token never
// moves, and re-setting an unchanged deadline stays a success rather than being
// read as a refusal.
func TestFolderShareRepo_SetExpiry_LiveLinkStaysEditable(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderShareRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	const token = "livelinkfolder000000000000000001"
	folder := folderShareLink(t, pool, user.ID, "live-link", token)

	first := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	require.NoError(t, repo.SetExpiry(ctx, user.ID, folder.ID, &first),
		"adding a deadline to a live link must succeed")
	stored := folderDeadline(t, pool, folder.ID)
	require.NotNil(t, stored)
	assert.True(t, first.Equal(stored.UTC()))

	require.NoError(t, repo.SetExpiry(ctx, user.ID, folder.ID, &first),
		"re-setting an unchanged deadline must stay a success")

	second := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	require.NoError(t, repo.SetExpiry(ctx, user.ID, folder.ID, &second))
	stored = folderDeadline(t, pool, folder.ID)
	require.NotNil(t, stored)
	assert.True(t, second.Equal(stored.UTC()))

	require.NoError(t, repo.SetExpiry(ctx, user.ID, folder.ID, nil),
		"clearing a live link's deadline must succeed")
	assert.Nil(t, folderDeadline(t, pool, folder.ID))

	assert.Equal(t, token, folderToken(t, pool, folder.ID),
		"no deadline edit may rotate the token")
}

// TestFolderShareRepo_SetExpiry_ExpiredLinkIsRefused: expiry is final, for both
// write shapes. Clearing is included because NULLing the deadline is the stronger
// revival — it makes the dead link immortal rather than extending it.
func TestFolderShareRepo_SetExpiry_ExpiredLinkIsRefused(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderShareRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	const token = "deadlinkfolder000000000000000001"
	folder := folderShareLink(t, pool, user.ID, "dead-link", token)

	deadline := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	res, err := pool.WriteDB.Exec(`UPDATE folder_shares SET expires_at = ? WHERE folder_id = ?`,
		deadline, folder.ID)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the fixture must have expired the link it then edits")

	t.Run("extending the deadline", func(t *testing.T) {
		future := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
		err := repo.SetExpiry(ctx, user.ID, folder.ID, &future)
		require.Error(t, err)
		assert.ErrorIs(t, err, apperror.ErrShareExpired)

		stored := folderDeadline(t, pool, folder.ID)
		require.NotNil(t, stored, "a refused write must leave the deadline alone")
		assert.True(t, deadline.Equal(stored.UTC()))
	})

	t.Run("clearing the deadline", func(t *testing.T) {
		err := repo.SetExpiry(ctx, user.ID, folder.ID, nil)
		require.Error(t, err, "nulling the deadline would make the dead link immortal")
		assert.ErrorIs(t, err, apperror.ErrShareExpired)

		stored := folderDeadline(t, pool, folder.ID)
		require.NotNil(t, stored, "a refused clear must leave the deadline in place, not NULL")
		assert.True(t, deadline.Equal(stored.UTC()))
	})

	assert.Equal(t, token, folderToken(t, pool, folder.ID),
		"a refused write may not rotate the token either")
}

// TestFolderShareRepo_SetExpiry_NoLinkIsNotFound keeps the other sentinel: a
// folder that is not shared has no deadline to move, and a caller must be told
// not-found rather than success.
func TestFolderShareRepo_SetExpiry_NoLinkIsNotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()

	folder, err := NewFolderRepo(pool.ReadDB, pool.WriteDB).Create(ctx, user.ID, nil, "unshared", "1/unshared")
	require.NoError(t, err)

	future := time.Now().UTC().Add(time.Hour)
	err = NewFolderShareRepo(pool.ReadDB, pool.WriteDB).SetExpiry(ctx, user.ID, folder.ID, &future)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

// TestFolderShareRepo_UpsertToken_ResetsDeadlineAndView is the rotation rule:
// both the deadline and the view stamp belong to the link that was just created.
// Without the deadline half, a re-share after an expiry — the only way back from
// a dead link — would be born dead and refused by the guard above.
func TestFolderShareRepo_UpsertToken_ResetsDeadlineAndView(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderShareRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	folder := folderShareLink(t, pool, user.ID, "rotating", "rotatingfolder0000000000000001")

	deadline := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	_, err := pool.WriteDB.Exec(
		`UPDATE folder_shares SET expires_at = ?, last_viewed_at = ? WHERE folder_id = ?`,
		deadline, time.Now().UTC().Add(-time.Minute), folder.ID)
	require.NoError(t, err)

	require.NoError(t, repo.UpsertToken(ctx, user.ID, folder.ID, "rotatingfolder0000000000000002"))

	assert.Nil(t, folderDeadline(t, pool, folder.ID),
		"the rotating statement must clear the previous link's deadline")
	var viewed sql.NullTime
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT last_viewed_at FROM folder_shares WHERE folder_id = ?`, folder.ID).Scan(&viewed))
	assert.False(t, viewed.Valid, "the rotating statement must clear the previous link's view stamp")

	// A fresh link is therefore editable, which is what makes re-sharing the way
	// back from an expired link.
	future := time.Now().UTC().Add(time.Hour)
	require.NoError(t, repo.SetExpiry(ctx, user.ID, folder.ID, &future))
}
