package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// fakeFolderRepo implements just enough of repository.FolderRepository for
// ancestor-chain walks in TokenAuthz. All methods except GetByID return
// nil/error — this keeps the test focused.
type fakeFolderRepo struct {
	byID map[int64]*model.Folder
}

func newFakeFolderRepo(folders ...*model.Folder) *fakeFolderRepo {
	m := make(map[int64]*model.Folder, len(folders))
	for _, f := range folders {
		m[f.ID] = f
	}
	return &fakeFolderRepo{byID: m}
}

func (r *fakeFolderRepo) Create(ctx context.Context, userID int64, parentID *int64, name, diskPath string) (*model.Folder, error) {
	return nil, nil
}
func (r *fakeFolderRepo) GetByID(ctx context.Context, userID, folderID int64) (*model.Folder, error) {
	f, ok := r.byID[folderID]
	if !ok || f.UserID != userID {
		return nil, apperror.ErrNotFound
	}
	return f, nil
}
func (r *fakeFolderRepo) GetByDiskPath(ctx context.Context, diskPath string) (*model.Folder, error) {
	return nil, nil
}
func (r *fakeFolderRepo) Tree(ctx context.Context, userID int64) ([]*model.FolderTreeItem, error) {
	return nil, nil
}
func (r *fakeFolderRepo) Rename(ctx context.Context, userID, folderID int64, newName, newDiskPath string) error {
	return nil
}
func (r *fakeFolderRepo) Move(ctx context.Context, userID, folderID int64, newParentID *int64, newDiskPath string) error {
	return nil
}
func (r *fakeFolderRepo) UpdateDescendantPaths(ctx context.Context, oldPrefix, newPrefix string) error {
	return nil
}
func (r *fakeFolderRepo) Delete(ctx context.Context, userID, folderID int64) error { return nil }

func i64p(v int64) *int64 { return &v }

func TestTokenAuthz_GlobalScope_AllowsEverywhere(t *testing.T) {
	ctx := context.Background()
	repo := newFakeFolderRepo()
	token := &model.APIToken{Scope: "readwrite"}
	a := NewTokenAuthz(token, nil)

	ok, err := a.CanRead(ctx, repo, 1, nil)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = a.CanWrite(ctx, repo, 1, nil)
	require.NoError(t, err)
	assert.True(t, ok)

	f := int64(42)
	ok, err = a.CanWrite(ctx, repo, 1, &f)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestTokenAuthz_GlobalRead_ReadOnly(t *testing.T) {
	ctx := context.Background()
	repo := newFakeFolderRepo()
	a := NewTokenAuthz(&model.APIToken{Scope: "read"}, nil)

	ok, err := a.CanRead(ctx, repo, 1, nil)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = a.CanWrite(ctx, repo, 1, nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestTokenAuthz_Scoped_DirectGrant(t *testing.T) {
	ctx := context.Background()
	repo := newFakeFolderRepo(&model.Folder{ID: 1, UserID: 1, ParentID: nil, Name: "Work"})
	token := &model.APIToken{Scope: "readwrite"}
	a := NewTokenAuthz(token, []model.TokenFolderPermission{
		{FolderID: i64p(1), Permission: "write"},
	})

	ok, err := a.CanWrite(ctx, repo, 1, i64p(1))
	require.NoError(t, err)
	assert.True(t, ok)

	// No access to root when only folder 1 was granted.
	ok, err = a.CanRead(ctx, repo, 1, nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestTokenAuthz_Scoped_AncestorCascade(t *testing.T) {
	ctx := context.Background()
	// Work/Projects/Q3 — grant is on Work, child folders should inherit.
	repo := newFakeFolderRepo(
		&model.Folder{ID: 1, UserID: 1, ParentID: nil, Name: "Work"},
		&model.Folder{ID: 2, UserID: 1, ParentID: i64p(1), Name: "Projects"},
		&model.Folder{ID: 3, UserID: 1, ParentID: i64p(2), Name: "Q3"},
	)
	a := NewTokenAuthz(&model.APIToken{Scope: "readwrite"}, []model.TokenFolderPermission{
		{FolderID: i64p(1), Permission: "write"},
	})

	for _, id := range []int64{1, 2, 3} {
		ok, err := a.CanWrite(ctx, repo, 1, i64p(id))
		require.NoError(t, err)
		assert.Truef(t, ok, "expected write on folder %d via ancestor", id)
	}
}

func TestTokenAuthz_Scoped_NearestAncestorWins(t *testing.T) {
	ctx := context.Background()
	// Work (write) / Projects (read) — Projects should NOT inherit write.
	repo := newFakeFolderRepo(
		&model.Folder{ID: 1, UserID: 1, ParentID: nil, Name: "Work"},
		&model.Folder{ID: 2, UserID: 1, ParentID: i64p(1), Name: "Projects"},
		&model.Folder{ID: 3, UserID: 1, ParentID: i64p(2), Name: "Q3"},
	)
	a := NewTokenAuthz(&model.APIToken{Scope: "readwrite"}, []model.TokenFolderPermission{
		{FolderID: i64p(1), Permission: "write"},
		{FolderID: i64p(2), Permission: "read"},
	})

	// Projects and Q3 fall under the "read" grant on Projects.
	for _, id := range []int64{2, 3} {
		ok, err := a.CanWrite(ctx, repo, 1, i64p(id))
		require.NoError(t, err)
		assert.Falsef(t, ok, "folder %d should be read-only under Projects grant", id)
		ok, err = a.CanRead(ctx, repo, 1, i64p(id))
		require.NoError(t, err)
		assert.True(t, ok)
	}
	// Work still has write.
	ok, err := a.CanWrite(ctx, repo, 1, i64p(1))
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestTokenAuthz_Scoped_RootGrant(t *testing.T) {
	ctx := context.Background()
	repo := newFakeFolderRepo(
		&model.Folder{ID: 1, UserID: 1, ParentID: nil, Name: "Work"},
	)
	a := NewTokenAuthz(&model.APIToken{Scope: "readwrite"}, []model.TokenFolderPermission{
		{FolderID: nil, Permission: "read"},
	})

	// Root readable, Work inherits root grant.
	ok, err := a.CanRead(ctx, repo, 1, nil)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = a.CanRead(ctx, repo, 1, i64p(1))
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = a.CanWrite(ctx, repo, 1, nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestTokenAuthz_Scoped_NoGrantDenies(t *testing.T) {
	ctx := context.Background()
	repo := newFakeFolderRepo(
		&model.Folder{ID: 1, UserID: 1, ParentID: nil, Name: "Work"},
		&model.Folder{ID: 2, UserID: 1, ParentID: nil, Name: "Private"},
	)
	a := NewTokenAuthz(&model.APIToken{Scope: "readwrite"}, []model.TokenFolderPermission{
		{FolderID: i64p(1), Permission: "write"},
	})

	ok, err := a.CanRead(ctx, repo, 1, i64p(2))
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestFilterReadableFolderIDs(t *testing.T) {
	tree := []*model.FolderTreeItem{
		{ID: 1, ParentID: nil, Name: "Work"},
		{ID: 2, ParentID: i64p(1), Name: "Projects"},
		{ID: 3, ParentID: nil, Name: "Private"},
	}
	a := NewTokenAuthz(&model.APIToken{Scope: "readwrite"}, []model.TokenFolderPermission{
		{FolderID: i64p(1), Permission: "read"},
	})

	readable, rootReadable := a.FilterReadableFolderIDs(tree)
	assert.False(t, rootReadable)
	assert.True(t, readable[1])
	assert.True(t, readable[2], "child should inherit read grant")
	assert.False(t, readable[3])
}

func TestSessionAuthz_AlwaysAllows(t *testing.T) {
	ctx := context.Background()
	repo := newFakeFolderRepo()
	a := SessionAuthz()
	ok, err := a.CanWrite(ctx, repo, 1, i64p(99999))
	require.NoError(t, err)
	assert.True(t, ok)
}
