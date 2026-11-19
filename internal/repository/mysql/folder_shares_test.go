package mysql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
