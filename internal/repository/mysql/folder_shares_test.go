package mysql

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
)

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
