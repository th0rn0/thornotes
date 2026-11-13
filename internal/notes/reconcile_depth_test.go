package notes_test

// reconcile's folder ordering (ticket 472, bullet 3).
//
// reconcile.go orders two folder passes by pathDepth: inserts shallowest first
// (step 7, so a parent row exists before a child references it) and deletes
// deepest first (step 6). The repair pass (step 3) walks the folder tree and
// resolves each stale row against its parent's *current* disk_path, so it too
// can only be right when the parent has been reconciled first. Until now no test
// ran any of that below one level, so a two- or three-deep tree — the normal
// shape of a real vault — could have broken silently.
//
// The ordering assertion cannot be made from the result counters alone: they say
// how many folders were created, not in which order, and a tree that happens to
// come out right one pass tells you nothing about the next. So the test wraps the
// folder repository (the service takes it as an interface, so this is a test-side
// seam, not a production one) and records the sequence the reconciler actually
// issued. The invariant asserted is the one the sort exists for:
//
//	a folder is never reconciled before a folder that must exist for it.
//
// Reversing a comparator drops the child folders entirely, which is what the
// failure looks like in production too: "reconcile: skip folder with missing
// parent".

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository"
	sqlite_repo "github.com/th0rn0/thornotes/internal/repository/sqlite"
)

// recordingFolders wraps a FolderRepository and records the disk_path of every
// folder the reconciler creates or renames, in call order.
type recordingFolders struct {
	repository.FolderRepository
	created []string
	renamed []string
}

func (r *recordingFolders) Create(ctx context.Context, userID int64, parentID *int64, name, diskPath string) (*model.Folder, error) {
	r.created = append(r.created, diskPath)
	return r.FolderRepository.Create(ctx, userID, parentID, name, diskPath)
}

func (r *recordingFolders) Rename(ctx context.Context, userID, folderID int64, newName, newDiskPath string) error {
	r.renamed = append(r.renamed, newDiskPath)
	return r.FolderRepository.Rename(ctx, userID, folderID, newName, newDiskPath)
}

// depthStack is a service stack whose folder writes are observable.
type depthStack struct {
	*serviceStack
	folders *recordingFolders
}

func newDepthStack(t *testing.T) *depthStack {
	t.Helper()
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	fsDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(fsDir)
	require.NoError(t, err)

	userRepo := sqlite_repo.NewUserRepo(pool.WriteDB)
	folders := &recordingFolders{FolderRepository: sqlite_repo.NewFolderRepo(pool.ReadDB, pool.WriteDB)}
	noteRepo := sqlite_repo.NewNoteRepo(pool.ReadDB, pool.WriteDB)

	ctx := context.Background()
	user, err := userRepo.Create(ctx, "depthuser", "hash", "depth-uuid")
	require.NoError(t, err)

	svc := notes.NewService(
		noteRepo, folders,
		sqlite_repo.NewSearchRepo(pool.ReadDB, pool.WriteDB),
		sqlite_repo.NewJournalRepo(pool.ReadDB, pool.WriteDB),
		sqlite_repo.NewLinkRepo(pool.ReadDB, pool.WriteDB),
		fs,
	)
	return &depthStack{
		serviceStack: &serviceStack{svc: svc, user: user, userID: user.ID, pool: pool, notesDir: fsDir},
		folders:      folders,
	}
}

// mknote writes a .md file at rel, relative to the user's own directory.
func (s *depthStack) mknote(t *testing.T, rel, body string) {
	t.Helper()
	abs := filepath.Join(s.notesDir, s.user.UUID, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o700))
	require.NoError(t, os.WriteFile(abs, []byte(body), 0o600))
}

type folderShape struct {
	ID       int64
	ParentID *int64
	Name     string
}

func (s *depthStack) folderShapes(t *testing.T) map[string]folderShape {
	t.Helper()
	tree, err := s.svc.FolderTree(t.Context(), s.userID)
	require.NoError(t, err)
	out := map[string]folderShape{}
	for _, f := range tree {
		out[f.DiskPath] = folderShape{ID: f.ID, ParentID: f.ParentID, Name: f.Name}
	}
	return out
}

// TestReconcile_ThreeDeepFolderCreationOrdersParentsFirst builds the tree a real
// vault has — three folders deep, a note at every level — and asserts both that
// the pass produces it and that it created the folders parent-first.
func TestReconcile_ThreeDeepFolderCreationOrdersParentsFirst(t *testing.T) {
	st := newDepthStack(t)
	ctx := context.Background()

	st.mknote(t, "root-note.md", "# Root Note\n\nat the top")
	st.mknote(t, "Alpha/alpha-note.md", "# Alpha Note\n\none down")
	st.mknote(t, "Alpha/Beta/beta-note.md", "# Beta Note\n\ntwo down")
	st.mknote(t, "Alpha/Beta/Gamma/gamma-note.md", "# Gamma Note\n\nthree down")

	_, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)

	// ── the creation sequence is parent-first, by path ──────────────────────
	require.Equal(t, []string{
		filepath.Join(st.user.UUID, "Alpha"),
		filepath.Join(st.user.UUID, "Alpha", "Beta"),
		filepath.Join(st.user.UUID, "Alpha", "Beta", "Gamma"),
	}, st.folders.created,
		"folders must be created shallowest first; a child created before its parent is skipped for the whole pass")

	// ── the tree exists, three levels deep ──────────────────────────────────
	shapes := st.folderShapes(t)
	require.Len(t, shapes, 3, "Alpha, Alpha/Beta and Alpha/Beta/Gamma")

	alpha, ok := shapes[filepath.Join(st.user.UUID, "Alpha")]
	require.True(t, ok, "Alpha must exist; got %v", shapes)
	beta, ok := shapes[filepath.Join(st.user.UUID, "Alpha", "Beta")]
	require.True(t, ok, "Alpha/Beta must exist; got %v", shapes)
	gamma, ok := shapes[filepath.Join(st.user.UUID, "Alpha", "Beta", "Gamma")]
	require.True(t, ok, "Alpha/Beta/Gamma must exist; got %v", shapes)

	assert.Nil(t, alpha.ParentID, "Alpha is a root folder")
	require.NotNil(t, beta.ParentID, "Beta must have a parent")
	assert.Equal(t, alpha.ID, *beta.ParentID, "Beta's parent must be Alpha")
	require.NotNil(t, gamma.ParentID, "Gamma must have a parent")
	assert.Equal(t, beta.ID, *gamma.ParentID, "Gamma's parent must be Beta, not Alpha")

	// ── every note landed in the folder of its directory ────────────────────
	all, err := st.svc.ListAllNotes(ctx, st.userID)
	require.NoError(t, err)
	require.Len(t, all, 4, "a note at every level")

	byTitle := map[string]*model.NoteListItem{}
	for _, n := range all {
		byTitle[n.Title] = n
	}
	assert.Nil(t, byTitle["Root Note"].FolderID, "the root note belongs to no folder")
	for title, folderID := range map[string]int64{
		"Alpha Note": alpha.ID,
		"Beta Note":  beta.ID,
		"Gamma Note": gamma.ID,
	} {
		note, ok := byTitle[title]
		require.True(t, ok, "note %q must exist", title)
		require.NotNil(t, note.FolderID, "note %q must be in a folder", title)
		assert.Equal(t, folderID, *note.FolderID, "note %q is in the wrong folder", title)
	}
}

// TestReconcile_ThreeDeepRepairOrdersParentsFirst is the same invariant on the
// repair path: a top folder renamed on disk whose DB cascade failed leaves the
// whole subtree's disk_paths stale. Repairing a grandchild needs its parent's
// path to be current already, so a pass that visits them in the wrong order
// repairs the child and silently leaves the grandchild stale.
func TestReconcile_ThreeDeepRepairOrdersParentsFirst(t *testing.T) {
	st := newDepthStack(t)
	ctx := context.Background()

	// Build the tree through the service, so every row exists with a correct
	// disk_path: this is the state before the rename.
	alpha, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Alpha")
	require.NoError(t, err)
	beta, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, &alpha.ID, "Beta")
	require.NoError(t, err)
	gamma, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, &beta.ID, "Gamma")
	require.NoError(t, err)

	noteIDs := map[string]int64{}
	for _, spec := range []struct {
		title  string
		slug   string
		folder *model.Folder
	}{
		{"Alpha Note", "alpha-note", alpha},
		{"Beta Note", "beta-note", beta},
		{"Gamma Note", "gamma-note", gamma},
	} {
		n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &spec.folder.ID, spec.title, nil)
		require.NoError(t, err)
		noteIDs[spec.slug] = n.ID
	}

	// Rename the top folder on disk only. The DB row's name and disk_path move
	// (that is what a rename does when it works); the descendants' paths keep
	// the old prefix, which is exactly the cascade failure step 3 repairs.
	userDir := filepath.Join(st.notesDir, st.user.UUID)
	require.NoError(t, os.Rename(filepath.Join(userDir, "Alpha"), filepath.Join(userDir, "Zeta")))
	newTopPath := filepath.Join(st.user.UUID, "Zeta")
	_, err = st.pool.WriteDB.ExecContext(ctx,
		`UPDATE folders SET name = ?, disk_path = ? WHERE id = ?`, "Zeta", newTopPath, alpha.ID)
	require.NoError(t, err)

	st.folders.renamed = nil

	res, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)

	betaPath := filepath.Join(newTopPath, "Beta")
	gammaPath := filepath.Join(betaPath, "Gamma")

	// Both stale descendants are repaired in this one pass, parents first. The
	// public Reconcile reports the total drift events it repaired; here that is
	// 2 folder paths + 3 note paths, and nothing else was wrong with the tree.
	assert.Equal(t, 5, res,
		"one pass must repair 2 folder paths and 3 note paths, and find nothing else to do")
	assert.Equal(t, []string{betaPath, gammaPath}, st.folders.renamed,
		"repairs must run parent before child")

	shapes := st.folderShapes(t)
	require.Len(t, shapes, 3, "the rename must repair rows, not delete and recreate them")
	require.Contains(t, shapes, betaPath, "Beta's row must be at its new path; got %v", shapes)
	require.Contains(t, shapes, gammaPath, "Gamma's row must be at its new path; got %v", shapes)
	require.NotNil(t, shapes[betaPath].ParentID)
	require.NotNil(t, shapes[gammaPath].ParentID)
	assert.Equal(t, shapes[newTopPath].ID, *shapes[betaPath].ParentID)
	assert.Equal(t, shapes[betaPath].ID, *shapes[gammaPath].ParentID)

	// The notes moved with their folders, and none of them was lost.
	for slug, id := range noteIDs {
		got, err := st.svc.GetNote(ctx, st.userID, id)
		require.NoError(t, err, "note %s must still exist", slug)
		assert.Contains(t, got.DiskPath, "Zeta", "note %s must live under the renamed folder", slug)
	}
}

// TestReconcile_NestedRepairAfterReparent_DoesNotDeleteTheChild is the same
// invariant on a tree whose *ids* do not happen to be in depth order.
//
// Step 3 walks the rows FolderRepository.Tree returns, which is ordered by
// parent_id — for a tree built by creating parents first, that happens to be
// parent-before-child, and the repair only works because of it. Reparenting
// breaks the coincidence: a folder created early and moved under a folder created
// later keeps its smaller id, so Tree puts its children *before* it. If both are
// stale (the state a failed rename cascade leaves), the child is then reconciled
// against a parent path that has not been repaired yet, repairs nothing, and —
// because its own path is also missing on disk — is deleted along with its
// notes in the same pass. The files survive; the rows, tags and share tokens do
// not.
//
// The tree here is built exactly that way: Papa is created first and then moved
// under Golf, so Papa.id < Golf.id < Charlie.id and Tree yields Charlie first.
func TestReconcile_NestedRepairAfterReparent_DoesNotDeleteTheChild(t *testing.T) {
	st := newDepthStack(t)
	ctx := context.Background()

	papa, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Papa")
	require.NoError(t, err)
	golf, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Golf")
	require.NoError(t, err)
	// Papa moves under Golf and keeps its (smaller) id.
	require.NoError(t, st.svc.MoveFolder(ctx, st.userID, st.user.UUID, papa.ID, &golf.ID))

	charlie, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, &papa.ID, "Charlie")
	require.NoError(t, err)

	note, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &charlie.ID, "Deep Note", nil)
	require.NoError(t, err)

	// Precondition: the tree really does enumerate Charlie before Papa, which is
	// what makes this a different ordering case from the test above.
	tree, err := st.svc.FolderTree(ctx, st.userID)
	require.NoError(t, err)
	var order []string
	for _, f := range tree {
		order = append(order, f.Name)
	}
	require.Equal(t, []string{"Golf", "Charlie", "Papa"}, order,
		"precondition: FolderTree lists the moved child before its parent")

	// Rename the top folder on disk, move its row, and leave Papa/Charlie stale.
	userDir := filepath.Join(st.notesDir, st.user.UUID)
	require.NoError(t, os.Rename(filepath.Join(userDir, "Golf"), filepath.Join(userDir, "Hotel")))
	newTopPath := filepath.Join(st.user.UUID, "Hotel")
	_, err = st.pool.WriteDB.ExecContext(ctx,
		`UPDATE folders SET name = ?, disk_path = ? WHERE id = ?`, "Hotel", newTopPath, golf.ID)
	require.NoError(t, err)

	_, err = st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)

	papaPath := filepath.Join(newTopPath, "Papa")
	charliePath := filepath.Join(papaPath, "Charlie")

	shapes := st.folderShapes(t)
	require.Contains(t, shapes, papaPath, "Papa's row must be repaired, not deleted; got %v", shapes)
	require.Contains(t, shapes, charliePath,
		"Charlie's row must be repaired, not deleted — a child repaired after its parent is skipped and then reaped; got %v", shapes)

	got, err := st.svc.GetNote(ctx, st.userID, note.ID)
	require.NoError(t, err, "the note in the nested folder must survive the repair pass")
	assert.Equal(t, charliePath+string(filepath.Separator)+got.Slug+".md", got.DiskPath)
}
