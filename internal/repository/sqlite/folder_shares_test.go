package sqlite

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
