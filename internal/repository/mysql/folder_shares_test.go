package mysql

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

// The MariaDB leg of FolderShareRepo.ListShares (ticket 616, slice 3). It
// mirrors the SQLite suite on purpose: the statement is the same, but the join,
// the DATETIME scan into *time.Time and the way a NULL comes back are the
// driver's, not the statement's — so "it works on SQLite" is not evidence for
// this one.
//
// The endpoint-level behaviour (both kinds in one list, ordering, ownership) is
// asserted in internal/handler/share_list_kinds_test.go against whichever
// backend the suite is pointed at.

type mysqlFolderShareFixture struct {
	pool   *db.Pool
	repo   *FolderShareRepo
	user   *model.User
	other  *model.User
	folder *model.Folder
}

func newMySQLFolderShareFixture(t *testing.T) *mysqlFolderShareFixture {
	t.Helper()
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	other, err := NewUserRepo(pool.WriteDB).Create(context.Background(), "user2", "hash", "test-uuid-2")
	require.NoError(t, err)

	folder, err := NewFolderRepo(pool.WriteDB).Create(context.Background(), user.ID, nil, "work", "uuid/work")
	require.NoError(t, err)

	return &mysqlFolderShareFixture{
		pool:   pool,
		repo:   NewFolderShareRepo(pool.WriteDB),
		user:   user,
		other:  other,
		folder: folder,
	}
}

func TestMySQL_FolderShareRepo_ListShares_FolderRowShape(t *testing.T) {
	f := newMySQLFolderShareFixture(t)
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
	assert.Equal(t, "work", row.Title, "the folder's name comes from the folders join")
	assert.Equal(t, token[:8]+"…", row.TokenPrefix)
	assert.NotContains(t, row.TokenPrefix, token[8:], "the body of the token must not leak")
	// Both of these are NULL in the schema; on this driver a NULL scanned into
	// the wrong type is how a "no deadline" row silently becomes a zero time.
	assert.Nil(t, row.ExpiresAt)
	assert.Nil(t, row.LastViewedAt)
	assert.False(t, row.UpdatedAt.IsZero(), "created_at must read back, not be dropped")

	otherShares, err := f.repo.ListShares(ctx, f.other.ID)
	require.NoError(t, err)
	assert.Empty(t, otherShares, "another account's list is empty, and that is decided in SQL")
}

// TestMySQL_FolderShareRepo_ListShares_DeadlineReadsBack covers the column that
// the note half's MySQL suite found worth its own assertion: a DATETIME written
// with a UTC layout must read back as the same instant, otherwise the list shows
// a deadline that has already passed.
func TestMySQL_FolderShareRepo_ListShares_DeadlineReadsBack(t *testing.T) {
	f := newMySQLFolderShareFixture(t)
	ctx := context.Background()

	require.NoError(t, f.repo.UpsertToken(ctx, f.user.ID, f.folder.ID, strings.Repeat("cd", 16)))
	past := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	_, err := f.pool.WriteDB.Exec(`UPDATE folder_shares SET expires_at = ? WHERE folder_id = ?`, past, f.folder.ID)
	require.NoError(t, err)

	shares, err := f.repo.ListShares(ctx, f.user.ID)
	require.NoError(t, err)
	require.Len(t, shares, 1, "an expired link is still a link that was handed out")
	require.NotNil(t, shares[0].ExpiresAt)
	assert.True(t, shares[0].ExpiresAt.Equal(past),
		"the deadline must read back as the instant it was set to, got %s", shares[0].ExpiresAt)
}

// The MariaDB leg of the folder-share deadline, mirroring the SQLite suite.
//
// It is a separate test on purpose: the guard is a comparison between a bound
// time and a DATETIME column inside an UPDATE, and MariaDB truncates the bound
// value to the column's second precision while also counting CHANGED rows rather
// than matched ones — so "the SQLite suite proves it" is not evidence here, and
// the unchanged-deadline case in particular needs its own assertion.

func mysqlFolderShareLink(t *testing.T, pool *db.Pool, userID int64, name, token string) *model.Folder {
	t.Helper()
	ctx := context.Background()
	folder, err := NewFolderRepo(pool.WriteDB).Create(ctx, userID, nil, name, "1/"+name)
	require.NoError(t, err)

	require.NoError(t, NewFolderShareRepo(pool.WriteDB).UpsertToken(ctx, userID, folder.ID, token))
	return folder
}

func mysqlFolderDeadline(t *testing.T, pool *db.Pool, folderID int64) *time.Time {
	t.Helper()
	var expiresAt *time.Time
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT expires_at FROM folder_shares WHERE folder_id = ?`, folderID).Scan(&expiresAt))
	return expiresAt
}

func mysqlFolderToken(t *testing.T, pool *db.Pool, folderID int64) string {
	t.Helper()
	var token string
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT token FROM folder_shares WHERE folder_id = ?`, folderID).Scan(&token))
	return token
}

// TestMySQL_FolderShareRepo_SetExpiry_LiveLinkStaysEditable keeps the half that
// PATCH exists for, including the case this driver makes ambiguous: re-writing a
// deadline the row already holds changes zero rows, and that must stay success.
func TestMySQL_FolderShareRepo_SetExpiry_LiveLinkStaysEditable(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderShareRepo(pool.WriteDB)
	ctx := context.Background()

	const token = "livelinkfolder000000000000000001"
	folder := mysqlFolderShareLink(t, pool, user.ID, "live-link", token)

	first := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	require.NoError(t, repo.SetExpiry(ctx, user.ID, folder.ID, &first))
	stored := mysqlFolderDeadline(t, pool, folder.ID)
	require.NotNil(t, stored)
	assert.True(t, first.Equal(stored.UTC()))

	require.NoError(t, repo.SetExpiry(ctx, user.ID, folder.ID, &first),
		"re-setting an unchanged deadline must stay a success, not a refusal")

	second := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	require.NoError(t, repo.SetExpiry(ctx, user.ID, folder.ID, &second))
	stored = mysqlFolderDeadline(t, pool, folder.ID)
	require.NotNil(t, stored)
	assert.True(t, second.Equal(stored.UTC()))

	require.NoError(t, repo.SetExpiry(ctx, user.ID, folder.ID, nil))
	assert.Nil(t, mysqlFolderDeadline(t, pool, folder.ID))

	assert.Equal(t, token, mysqlFolderToken(t, pool, folder.ID),
		"no deadline edit may rotate the token")
}

// TestMySQL_FolderShareRepo_SetExpiry_ExpiredLinkIsRefused is the MariaDB leg of
// "expired is final", for both write shapes.
func TestMySQL_FolderShareRepo_SetExpiry_ExpiredLinkIsRefused(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderShareRepo(pool.WriteDB)
	ctx := context.Background()

	const token = "deadlinkfolder000000000000000001"
	folder := mysqlFolderShareLink(t, pool, user.ID, "dead-link", token)

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
		assert.ErrorIs(t, err, apperror.ErrShareExpired,
			"on a dead link the guard must win over the driver's changed-rows answer")

		stored := mysqlFolderDeadline(t, pool, folder.ID)
		require.NotNil(t, stored, "a refused write must leave the deadline alone")
		assert.True(t, deadline.Equal(stored.UTC()))
	})

	t.Run("clearing the deadline", func(t *testing.T) {
		err := repo.SetExpiry(ctx, user.ID, folder.ID, nil)
		require.Error(t, err, "nulling the deadline would make the dead link immortal")
		assert.ErrorIs(t, err, apperror.ErrShareExpired)
		assert.NotNil(t, mysqlFolderDeadline(t, pool, folder.ID),
			"a refused clear must leave the deadline in place, not NULL")
	})

	assert.Equal(t, token, mysqlFolderToken(t, pool, folder.ID))
}

// TestMySQL_FolderShareRepo_SetExpiry_NoLinkIsNotFound: no row is not-found, not
// a silent success on a deadline nothing reads.
func TestMySQL_FolderShareRepo_SetExpiry_NoLinkIsNotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	ctx := context.Background()

	folder, err := NewFolderRepo(pool.WriteDB).Create(ctx, user.ID, nil, "unshared", "1/unshared")
	require.NoError(t, err)

	future := time.Now().UTC().Add(time.Hour)
	err = NewFolderShareRepo(pool.WriteDB).SetExpiry(ctx, user.ID, folder.ID, &future)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

// TestMySQL_FolderShareRepo_UpsertToken_ResetsDeadlineAndView: the rotating
// statement clears both the deadline and the view stamp, which is what lets a
// re-share — the only way back from an expired link — land on an editable row.
func TestMySQL_FolderShareRepo_UpsertToken_ResetsDeadlineAndView(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderShareRepo(pool.WriteDB)
	ctx := context.Background()

	folder := mysqlFolderShareLink(t, pool, user.ID, "rotating", "rotatingfolder0000000000000001")

	deadline := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	_, err := pool.WriteDB.Exec(
		`UPDATE folder_shares SET expires_at = ?, last_viewed_at = ? WHERE folder_id = ?`,
		deadline, time.Now().UTC().Add(-time.Minute), folder.ID)
	require.NoError(t, err)

	require.NoError(t, repo.UpsertToken(ctx, user.ID, folder.ID, "rotatingfolder0000000000000002"))

	assert.Nil(t, mysqlFolderDeadline(t, pool, folder.ID),
		"the rotating statement must clear the previous link's deadline")
	var viewed sql.NullTime
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT last_viewed_at FROM folder_shares WHERE folder_id = ?`, folder.ID).Scan(&viewed))
	assert.False(t, viewed.Valid, "the rotating statement must clear the previous link's view stamp")

	future := time.Now().UTC().Add(time.Hour)
	require.NoError(t, repo.SetExpiry(ctx, user.ID, folder.ID, &future),
		"a link created by re-sharing must be editable")
}
