package notes_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository"
	sqlite_repo "github.com/th0rn0/thornotes/internal/repository/sqlite"
)

// =============================================================================
// C-01: Duplicate note creation (root, folder, concurrent, and import collision)
// must return 409 Conflict while preserving the original file, content, hash,
// DB row, tags, and share token, and must survive subsequent Reconcile.
// =============================================================================

func TestP0_C01_CreateNote_DuplicateAtRoot_PreservesFileDBTagsShareToken(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	n1, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Root Target", []string{"alpha", "beta"})
	require.NoError(t, err)

	origContent := "# Root Target\n\nOriginal root note body that must survive."
	origHash, err := st.svc.UpdateNoteContent(ctx, st.userID, n1.ID, origContent, n1.ContentHash)
	require.NoError(t, err)

	shareToken, err := st.svc.SetShareToken(ctx, st.userID, n1.ID, false)
	require.NoError(t, err)
	require.NotNil(t, shareToken)

	// Second CreateNote at root with same title (and another title that slugifies to the same slug).
	_, errDupExact := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Root Target", []string{"clobber"})
	require.Error(t, errDupExact)
	assert.True(t, apperror.IsConflict(errDupExact), "expected conflict, got %v", errDupExact)

	_, errDupSlug := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "  root---target! ", []string{"clobber"})
	require.Error(t, errDupSlug)
	assert.True(t, apperror.IsConflict(errDupSlug), "expected conflict on slug collision, got %v", errDupSlug)

	// Verify file on disk still exists with exact original content.
	absPath := filepath.Join(st.notesDir, n1.DiskPath)
	diskBytes, err := os.ReadFile(absPath)
	require.NoError(t, err, "original note file on disk must not be deleted")
	assert.Equal(t, origContent, string(diskBytes), "original note file on disk must not be truncated/overwritten")

	// Verify Reconcile does not delete or alter the note.
	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Equal(t, 0, changes, "Reconcile must see zero drift after rejected duplicate CreateNote")

	// Verify DB row, content, hash, tags, and share token all survived.
	got, err := st.svc.GetNote(ctx, st.userID, n1.ID)
	require.NoError(t, err)
	assert.Equal(t, n1.ID, got.ID)
	assert.Equal(t, "Root Target", got.Title)
	assert.Equal(t, origContent, got.Content)
	assert.Equal(t, origHash, got.ContentHash)
	assert.Equal(t, []string{"alpha", "beta"}, got.Tags)
	require.NotNil(t, got.ShareToken)
	assert.Equal(t, *shareToken, *got.ShareToken)
}

func TestP0_C01_CreateNote_DuplicateInFolder_PreservesFileDBTagsShareToken(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	folder, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Engineering")
	require.NoError(t, err)

	n1, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &folder.ID, "Runbook", []string{"ops", "p0"})
	require.NoError(t, err)

	origContent := "# Runbook\n\nProduction runbook steps."
	origHash, err := st.svc.UpdateNoteContent(ctx, st.userID, n1.ID, origContent, n1.ContentHash)
	require.NoError(t, err)

	shareToken, err := st.svc.SetShareToken(ctx, st.userID, n1.ID, false)
	require.NoError(t, err)
	require.NotNil(t, shareToken)

	// Duplicate CreateNote in the same folder.
	_, errDup := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &folder.ID, "Runbook", nil)
	require.Error(t, errDup)
	assert.True(t, apperror.IsConflict(errDup))

	absPath := filepath.Join(st.notesDir, n1.DiskPath)
	diskBytes, err := os.ReadFile(absPath)
	require.NoError(t, err, "folder note file on disk must not be deleted")
	assert.Equal(t, origContent, string(diskBytes))

	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Equal(t, 0, changes)

	got, err := st.svc.GetNote(ctx, st.userID, n1.ID)
	require.NoError(t, err)
	assert.Equal(t, origContent, got.Content)
	assert.Equal(t, origHash, got.ContentHash)
	assert.Equal(t, []string{"ops", "p0"}, got.Tags)
	require.NotNil(t, got.ShareToken)
	assert.Equal(t, *shareToken, *got.ShareToken)
}

func TestP0_C01_ImportCollisions_PreserveExistingNotesAndFolderTrees(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	// 1. Seed a root note and a folder with a note + subfolder note.
	rootNote, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "readme", []string{"root-tag"})
	require.NoError(t, err)
	rootContent := "# Original Readme\n\nDo not clobber."
	rootHash, err := st.svc.UpdateNoteContent(ctx, st.userID, rootNote.ID, rootContent, rootNote.ContentHash)
	require.NoError(t, err)
	rootShare, err := st.svc.SetShareToken(ctx, st.userID, rootNote.ID, false)
	require.NoError(t, err)

	workFolder, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Work")
	require.NoError(t, err)
	subFolder, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, &workFolder.ID, "Sub")
	require.NoError(t, err)

	workNote, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &workFolder.ID, "todo", []string{"work-tag"})
	require.NoError(t, err)
	workContent := "# Original Work Todo\n\nMust survive zip import collision."
	workHash, err := st.svc.UpdateNoteContent(ctx, st.userID, workNote.ID, workContent, workNote.ContentHash)
	require.NoError(t, err)
	workShare, err := st.svc.SetShareToken(ctx, st.userID, workNote.ID, false)
	require.NoError(t, err)

	subNote, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &subFolder.ID, "deep", []string{"sub-tag"})
	require.NoError(t, err)
	subContent := "# Deep Note\n\nInside Work/Sub."
	subHash, err := st.svc.UpdateNoteContent(ctx, st.userID, subNote.ID, subContent, subNote.ContentHash)
	require.NoError(t, err)

	// 2. ImportMarkdown colliding with existing root note "readme.md".
	_, err = st.svc.ImportMarkdown(ctx, st.userID, st.user.UUID, "readme.md", "# Clobbered by ImportMarkdown")
	require.Error(t, err)
	assert.True(t, apperror.IsConflict(err))

	// 3. ImportZip colliding with existing folder "Work" and note "Work/todo.md", plus adding a new note "Work/new-note.md".
	zipData := makeZip(t, map[string]string{
		"Work/todo.md":     "# Clobbered by ImportZip",
		"Work/new-note.md": "# Newly Imported Note",
		"readme.md":        "# Clobbered Root by ImportZip",
	})
	res, err := st.svc.ImportZip(ctx, st.userID, st.user.UUID, zipData)
	require.NoError(t, err)
	assert.Equal(t, 1, res.NotesCreated, "only the non-colliding note should be created")
	assert.Equal(t, 0, res.FoldersCreated, "Work folder already existed")

	// Verify disk files and Reconcile.
	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Equal(t, 0, changes, "Reconcile must find zero missing/corrupted files after import collisions")

	gotRoot, err := st.svc.GetNote(ctx, st.userID, rootNote.ID)
	require.NoError(t, err)
	assert.Equal(t, rootContent, gotRoot.Content)
	assert.Equal(t, rootHash, gotRoot.ContentHash)
	assert.Equal(t, []string{"root-tag"}, gotRoot.Tags)
	require.NotNil(t, gotRoot.ShareToken)
	assert.Equal(t, *rootShare, *gotRoot.ShareToken)

	gotWork, err := st.svc.GetNote(ctx, st.userID, workNote.ID)
	require.NoError(t, err)
	assert.Equal(t, workContent, gotWork.Content)
	assert.Equal(t, workHash, gotWork.ContentHash)
	assert.Equal(t, []string{"work-tag"}, gotWork.Tags)
	require.NotNil(t, gotWork.ShareToken)
	assert.Equal(t, *workShare, *gotWork.ShareToken)

	gotSub, err := st.svc.GetNote(ctx, st.userID, subNote.ID)
	require.NoError(t, err)
	assert.Equal(t, subContent, gotSub.Content)
	assert.Equal(t, subHash, gotSub.ContentHash)
	assert.Equal(t, []string{"sub-tag"}, gotSub.Tags)
}

func TestP0_C01_CreateNote_ConcurrentRace_WinnerSurvivesOnDiskAndDB(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	const workers = 10
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, errs[idx] = st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Concurrent Note", []string{"race"})
		}(i)
	}
	close(start)
	wg.Wait()

	successes := 0
	conflicts := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if apperror.IsConflict(err) {
			conflicts++
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, workers-1, conflicts)

	absFile := filepath.Join(st.notesDir, st.user.UUID, "concurrent-note.md")
	_, statErr := os.Stat(absFile)
	require.NoError(t, statErr, "winning note file must exist on disk after concurrent CreateNote race")

	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Equal(t, 0, changes)
}

// =============================================================================
// C-02: Sequential and concurrent duplicate folder creates return 409 Conflict
// with the existing folder tree, files, and DB rows intact.
// =============================================================================

func TestP0_C02_CreateFolder_SequentialDuplicate_PreservesExistingTreeAndNotes(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	fRoot, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Projects")
	require.NoError(t, err)
	fChild, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, &fRoot.ID, "Sub")
	require.NoError(t, err)

	n1, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &fRoot.ID, "Roadmap", []string{"plan"})
	require.NoError(t, err)
	_, err = st.svc.UpdateNoteContent(ctx, st.userID, n1.ID, "Roadmap body", n1.ContentHash)
	require.NoError(t, err)

	n2, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &fChild.ID, "Specs", []string{"spec"})
	require.NoError(t, err)
	_, err = st.svc.UpdateNoteContent(ctx, st.userID, n2.ID, "Specs body", n2.ContentHash)
	require.NoError(t, err)

	// Duplicate CreateFolder at root ("Projects") and under fRoot ("Sub").
	_, errDupRoot := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Projects")
	require.Error(t, errDupRoot)
	assert.True(t, apperror.IsConflict(errDupRoot))

	_, errDupChild := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, &fRoot.ID, "Sub")
	require.Error(t, errDupChild)
	assert.True(t, apperror.IsConflict(errDupChild))

	// Disk files and directories must still exist with original content.
	b1, err := os.ReadFile(filepath.Join(st.notesDir, n1.DiskPath))
	require.NoError(t, err, "n1 file must still exist on disk")
	assert.Equal(t, "Roadmap body", string(b1))

	b2, err := os.ReadFile(filepath.Join(st.notesDir, n2.DiskPath))
	require.NoError(t, err, "n2 file in subfolder must still exist on disk")
	assert.Equal(t, "Specs body", string(b2))

	// Reconcile must find zero drift and keep all folders and notes intact.
	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Equal(t, 0, changes)

	folders, err := st.svc.FolderTree(ctx, st.userID)
	require.NoError(t, err)
	assert.Len(t, folders, 2)

	notes, err := st.svc.ListAllNotes(ctx, st.userID)
	require.NoError(t, err)
	assert.Len(t, notes, 2)
}

func TestP0_C02_CreateFolder_ConcurrentDuplicate_PreservesWinnerDirectoryAndDBRow(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	const workers = 10
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, errs[idx] = st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "RaceFolder")
		}(i)
	}
	close(start)
	wg.Wait()

	successes := 0
	conflicts := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if apperror.IsConflict(err) {
			conflicts++
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, workers-1, conflicts)

	absFolder := filepath.Join(st.notesDir, st.user.UUID, "RaceFolder")
	info, statErr := os.Stat(absFolder)
	require.NoError(t, statErr, "winning folder directory on disk must not be deleted by losing concurrent callers")
	assert.True(t, info.IsDir())

	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Equal(t, 0, changes)

	tree, err := st.svc.FolderTree(ctx, st.userID)
	require.NoError(t, err)
	require.Len(t, tree, 1)
	assert.Equal(t, "RaceFolder", tree[0].Name)
}

// interleavingFolderRepo deterministically forces the exact interleaving:
//  1. Caller A passes GetByDiskPath (ErrNotFound) and wins CreateDirExclusive.
//  2. Caller A enters folders.Create and pauses before the DB INSERT.
//  3. Caller B runs CreateFolder: passes GetByDiskPath (ErrNotFound), gets
//     os.ErrExist on CreateDirExclusive, enters folders.Create, and wins the DB INSERT.
//  4. Caller A resumes in folders.Create, hits UNIQUE constraint (409 Conflict),
//     and must NOT remove the directory now owned by Caller B's winning DB row.
type interleavingFolderRepo struct {
	repository.FolderRepository
	createCalls   atomic.Int32
	callerAInDB   chan struct{}
	callerBDoneDB chan struct{}
}

func (r *interleavingFolderRepo) Create(ctx context.Context, userID int64, parentID *int64, name, diskPath string) (*model.Folder, error) {
	callNum := r.createCalls.Add(1)
	if callNum == 1 {
		// Caller A: already won CreateDirExclusive on disk; signal B to start,
		// then wait until B has won folders.Create in the DB.
		close(r.callerAInDB)
		<-r.callerBDoneDB
		return r.FolderRepository.Create(ctx, userID, parentID, name, diskPath)
	}
	if callNum == 2 {
		// Caller B: got os.ErrExist on CreateDirExclusive; now wins folders.Create.
		f, err := r.FolderRepository.Create(ctx, userID, parentID, name, diskPath)
		close(r.callerBDoneDB)
		return f, err
	}
	return r.FolderRepository.Create(ctx, userID, parentID, name, diskPath)
}

func TestP0_C02_CreateFolder_DeterministicInterleaving_CallerAWinsDiskCallerBWinsDB(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	hookRepo := &interleavingFolderRepo{
		FolderRepository: st.svc.Folders(),
		callerAInDB:      make(chan struct{}),
		callerBDoneDB:    make(chan struct{}),
	}
	noteRepo := sqlite_repo.NewNoteRepo(st.pool.ReadDB, st.pool.WriteDB)
	searchRepo := sqlite_repo.NewSearchRepo(st.pool.ReadDB, st.pool.WriteDB)
	journalRepo := sqlite_repo.NewJournalRepo(st.pool.ReadDB, st.pool.WriteDB)

	// H-10 added a per-Service tree lock, so two goroutines sharing one Service
	// are serialised and can no longer interleave by construction. The
	// interleaving this test guards against is the cross-process one (two
	// thornotes processes over one notes root and one database), so model that
	// honestly: two Services, each with its own locks, over the shared repos.
	svc := notes.NewService(noteRepo, hookRepo, searchRepo, journalRepo, st.svc.FileStore())
	svcB := notes.NewService(noteRepo, hookRepo, searchRepo, journalRepo, st.svc.FileStore())

	var (
		folderA, folderB *model.Folder
		errA, errB       error
		wg               sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		folderA, errA = svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Interleaved")
	}()
	go func() {
		defer wg.Done()
		// Wait until Caller A has created the directory on disk and entered folders.Create.
		<-hookRepo.callerAInDB
		folderB, errB = svcB.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Interleaved")
	}()
	wg.Wait()

	// Caller B must succeed and own the DB row; Caller A must receive 409 Conflict.
	require.NoError(t, errB, "Caller B (DB winner) must succeed")
	require.NotNil(t, folderB)
	require.Error(t, errA, "Caller A (disk creator, DB loser) must receive conflict")
	assert.True(t, apperror.IsConflict(errA), "expected conflict error for Caller A, got %v", errA)
	assert.Nil(t, folderA)

	// The directory created by Caller A (now owned by Caller B's DB row) must still exist on disk.
	absFolder := filepath.Join(st.notesDir, folderB.DiskPath)
	info, statErr := os.Stat(absFolder)
	require.NoError(t, statErr, "Caller A must NOT remove the directory after Caller B won folders.Create")
	assert.True(t, info.IsDir())

	// Reconcile must see zero drift and keep Caller B's folder intact.
	changes, err := svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Equal(t, 0, changes)

	tree, err := svc.FolderTree(ctx, st.userID)
	require.NoError(t, err)
	require.Len(t, tree, 1)
	assert.Equal(t, folderB.ID, tree[0].ID)
	assert.Equal(t, "Interleaved", tree[0].Name)
}

// =============================================================================
// Sibling collision paths: UpdateNoteMetadata (rename to existing slug in same
// folder) and MoveNote (move into folder that already has a note with the same
// slug) must return 409 Conflict without clobbering or deleting either the
// source or the existing target file, content, hash, DB row, tags, or share token.
// =============================================================================

func TestP0_C01_UpdateNoteMetadata_RenameCollision_PreservesSourceAndTarget(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	// Create Target note and Source note in the same folder (and also test at root).
	target, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Target", []string{"target-tag"})
	require.NoError(t, err)
	targetContent := "# Target\n\nExisting target content must survive."
	targetHash, err := st.svc.UpdateNoteContent(ctx, st.userID, target.ID, targetContent, target.ContentHash)
	require.NoError(t, err)
	targetShare, err := st.svc.SetShareToken(ctx, st.userID, target.ID, false)
	require.NoError(t, err)
	require.NotNil(t, targetShare)

	source, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Source", []string{"source-tag"})
	require.NoError(t, err)
	sourceContent := "# Source\n\nOriginal source content must survive."
	sourceHash, err := st.svc.UpdateNoteContent(ctx, st.userID, source.ID, sourceContent, source.ContentHash)
	require.NoError(t, err)
	sourceShare, err := st.svc.SetShareToken(ctx, st.userID, source.ID, false)
	require.NoError(t, err)
	require.NotNil(t, sourceShare)

	// Attempt to rename Source -> "Target" (and "  target! " which slugifies to "target").
	err = st.svc.UpdateNoteMetadata(ctx, st.userID, st.user.UUID, source.ID, "Target", []string{"clobbered-tag"})
	require.Error(t, err)
	assert.True(t, apperror.IsConflict(err), "expected 409 conflict on rename collision, got %v", err)

	// Both files on disk must still exist with their original contents.
	targetBytes, err := os.ReadFile(filepath.Join(st.notesDir, target.DiskPath))
	require.NoError(t, err, "existing target file must survive rejected rename")
	assert.Equal(t, targetContent, string(targetBytes))

	sourceBytes, err := os.ReadFile(filepath.Join(st.notesDir, source.DiskPath))
	require.NoError(t, err, "source file must remain at original path after rejected rename")
	assert.Equal(t, sourceContent, string(sourceBytes))

	// Reconcile must see zero drift and delete nothing.
	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Equal(t, 0, changes)

	// Both DB rows, contents, hashes, tags, and share tokens must be untouched.
	gotTarget, err := st.svc.GetNote(ctx, st.userID, target.ID)
	require.NoError(t, err)
	assert.Equal(t, "Target", gotTarget.Title)
	assert.Equal(t, "target", gotTarget.Slug)
	assert.Equal(t, targetContent, gotTarget.Content)
	assert.Equal(t, targetHash, gotTarget.ContentHash)
	assert.Equal(t, []string{"target-tag"}, gotTarget.Tags)
	require.NotNil(t, gotTarget.ShareToken)
	assert.Equal(t, *targetShare, *gotTarget.ShareToken)

	gotSource, err := st.svc.GetNote(ctx, st.userID, source.ID)
	require.NoError(t, err)
	assert.Equal(t, "Source", gotSource.Title)
	assert.Equal(t, "source", gotSource.Slug)
	assert.Equal(t, sourceContent, gotSource.Content)
	assert.Equal(t, sourceHash, gotSource.ContentHash)
	assert.Equal(t, []string{"source-tag"}, gotSource.Tags)
	require.NotNil(t, gotSource.ShareToken)
	assert.Equal(t, *sourceShare, *gotSource.ShareToken)
}

func TestP0_C01_MoveNote_DestinationCollision_PreservesSourceAndTarget(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	destFolder, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Destination")
	require.NoError(t, err)

	// Target note inside Destination folder.
	target, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &destFolder.ID, "Same Title", []string{"dest-tag"})
	require.NoError(t, err)
	targetContent := "# Target in Destination\n\nMust not be overwritten by MoveNote."
	targetHash, err := st.svc.UpdateNoteContent(ctx, st.userID, target.ID, targetContent, target.ContentHash)
	require.NoError(t, err)
	targetShare, err := st.svc.SetShareToken(ctx, st.userID, target.ID, false)
	require.NoError(t, err)
	require.NotNil(t, targetShare)

	// Source note at root with the same slug ("same-title").
	source, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Same Title", []string{"root-tag"})
	require.NoError(t, err)
	sourceContent := "# Source at Root\n\nMust stay at root when move conflicts."
	sourceHash, err := st.svc.UpdateNoteContent(ctx, st.userID, source.ID, sourceContent, source.ContentHash)
	require.NoError(t, err)
	sourceShare, err := st.svc.SetShareToken(ctx, st.userID, source.ID, false)
	require.NoError(t, err)
	require.NotNil(t, sourceShare)

	// Attempt to move Source into Destination folder -> must return 409 Conflict.
	err = st.svc.MoveNote(ctx, st.userID, st.user.UUID, source.ID, &destFolder.ID)
	require.Error(t, err)
	assert.True(t, apperror.IsConflict(err), "expected 409 conflict on move collision, got %v", err)

	// Both files on disk must still exist with their original contents.
	targetBytes, err := os.ReadFile(filepath.Join(st.notesDir, target.DiskPath))
	require.NoError(t, err, "existing target file in destination folder must survive rejected move")
	assert.Equal(t, targetContent, string(targetBytes))

	sourceBytes, err := os.ReadFile(filepath.Join(st.notesDir, source.DiskPath))
	require.NoError(t, err, "source file at root must survive rejected move")
	assert.Equal(t, sourceContent, string(sourceBytes))

	// Reconcile must see zero drift and delete nothing.
	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Equal(t, 0, changes)

	// Both DB rows, contents, hashes, tags, and share tokens must be untouched.
	gotTarget, err := st.svc.GetNote(ctx, st.userID, target.ID)
	require.NoError(t, err)
	require.NotNil(t, gotTarget.FolderID)
	assert.Equal(t, destFolder.ID, *gotTarget.FolderID)
	assert.Equal(t, targetContent, gotTarget.Content)
	assert.Equal(t, targetHash, gotTarget.ContentHash)
	assert.Equal(t, []string{"dest-tag"}, gotTarget.Tags)
	require.NotNil(t, gotTarget.ShareToken)
	assert.Equal(t, *targetShare, *gotTarget.ShareToken)

	gotSource, err := st.svc.GetNote(ctx, st.userID, source.ID)
	require.NoError(t, err)
	assert.Nil(t, gotSource.FolderID)
	assert.Equal(t, sourceContent, gotSource.Content)
	assert.Equal(t, sourceHash, gotSource.ContentHash)
	assert.Equal(t, []string{"root-tag"}, gotSource.Tags)
	require.NotNil(t, gotSource.ShareToken)
	assert.Equal(t, *sourceShare, *gotSource.ShareToken)
}

func TestP0_C01_RenameAndMove_ConcurrentRaceAgainstPreExistingDiskFile_PreservesTarget(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	// Create source note at root and a destination folder.
	source, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Source Race", nil)
	require.NoError(t, err)
	_, err = st.svc.UpdateNoteContent(ctx, st.userID, source.ID, "source body", source.ContentHash)
	require.NoError(t, err)

	destFolder, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "DestRace")
	require.NoError(t, err)

	// Simulate a concurrent winner or unindexed file already present on disk at the destination paths
	// (so GetByFolderAndSlug returns ErrNotFound, exercising the RenameFileExclusive os.Link EEXIST guard).
	unindexedRenameTarget := filepath.Join(st.notesDir, st.user.UUID, "colliding-disk-only.md")
	require.NoError(t, os.WriteFile(unindexedRenameTarget, []byte("disk-only target 1"), 0600))

	err = st.svc.UpdateNoteMetadata(ctx, st.userID, st.user.UUID, source.ID, "Colliding Disk Only", nil)
	require.Error(t, err)
	assert.True(t, apperror.IsConflict(err), "RenameFileExclusive must return 409 Conflict when destination file exists on disk")

	b1, err := os.ReadFile(unindexedRenameTarget)
	require.NoError(t, err)
	assert.Equal(t, "disk-only target 1", string(b1), "disk-only target file must not be overwritten by rename")

	unindexedMoveTarget := filepath.Join(st.notesDir, destFolder.DiskPath, "source-race.md")
	require.NoError(t, os.WriteFile(unindexedMoveTarget, []byte("disk-only target 2"), 0600))

	err = st.svc.MoveNote(ctx, st.userID, st.user.UUID, source.ID, &destFolder.ID)
	require.Error(t, err)
	assert.True(t, apperror.IsConflict(err), "RenameFileExclusive must return 409 Conflict when destination file exists on disk during move")

	b2, err := os.ReadFile(unindexedMoveTarget)
	require.NoError(t, err)
	assert.Equal(t, "disk-only target 2", string(b2), "disk-only target file must not be overwritten by move")
}

// =============================================================================
// Folder rename/move destination collisions must return 409 Conflict without
// replacing or destroying an existing destination folder (os.Rename silently
// replaces an empty destination directory on Linux).
// =============================================================================

func TestP0_FolderRenameCollision_PreservesSourceAndDestination(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	src, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "OldDir")
	require.NoError(t, err)
	srcNote, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &src.ID, "keep", nil)
	require.NoError(t, err)
	_, err = st.svc.UpdateNoteContent(ctx, st.userID, srcNote.ID, "keep me", srcNote.ContentHash)
	require.NoError(t, err)

	// Existing empty destination folder with the target name.
	dst, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "NewDir")
	require.NoError(t, err)

	err = st.svc.RenameFolder(ctx, st.userID, st.user.UUID, src.ID, "NewDir")
	require.Error(t, err)
	assert.True(t, apperror.IsConflict(err), "expected conflict on folder rename collision, got %v", err)

	srcAbs := filepath.Join(st.notesDir, src.DiskPath)
	dstAbs := filepath.Join(st.notesDir, dst.DiskPath)
	for _, p := range []string{srcAbs, dstAbs} {
		info, statErr := os.Stat(p)
		require.NoError(t, statErr, "folder must survive rejected rename: %s", p)
		assert.True(t, info.IsDir())
	}

	// Note inside the source folder must still be on disk.
	_, err = os.ReadFile(filepath.Join(st.notesDir, srcNote.DiskPath))
	require.NoError(t, err, "note inside source folder must survive rejected rename")

	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Equal(t, 0, changes)
}

func TestP0_FolderMoveCollision_PreservesSourceAndDestination(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	src, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Src")
	require.NoError(t, err)
	srcNote, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &src.ID, "note", nil)
	require.NoError(t, err)
	_, err = st.svc.UpdateNoteContent(ctx, st.userID, srcNote.ID, "src note", srcNote.ContentHash)
	require.NoError(t, err)

	dest, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Dest")
	require.NoError(t, err)
	// Destination parent already contains a folder with the same name.
	destSrc, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, &dest.ID, "Src")
	require.NoError(t, err)

	err = st.svc.MoveFolder(ctx, st.userID, st.user.UUID, src.ID, &dest.ID)
	require.Error(t, err)
	assert.True(t, apperror.IsConflict(err), "expected conflict on folder move collision, got %v", err)

	srcAbs := filepath.Join(st.notesDir, src.DiskPath)
	destSrcAbs := filepath.Join(st.notesDir, destSrc.DiskPath)
	for _, p := range []string{srcAbs, destSrcAbs} {
		info, statErr := os.Stat(p)
		require.NoError(t, statErr, "folder must survive rejected move: %s", p)
		assert.True(t, info.IsDir())
	}

	_, err = os.ReadFile(filepath.Join(st.notesDir, srcNote.DiskPath))
	require.NoError(t, err, "note inside source folder must survive rejected move")

	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Equal(t, 0, changes)
}

func TestP0_FolderRenameSameName_IsNoOp(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	f, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "SameName")
	require.NoError(t, err)

	// Renaming to its existing name must be a no-op success, not a 409.
	err = st.svc.RenameFolder(ctx, st.userID, st.user.UUID, f.ID, "SameName")
	require.NoError(t, err, "same-name folder rename must be a no-op, got %v", err)

	abs := filepath.Join(st.notesDir, f.DiskPath)
	info, statErr := os.Stat(abs)
	require.NoError(t, statErr, "folder must still exist after no-op rename")
	assert.True(t, info.IsDir())

	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Equal(t, 0, changes)
}

func TestP0_FolderRenameDiskOnlyDestination_ReturnsConflictAndPreservesBoth(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	src, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "SrcDir")
	require.NoError(t, err)
	srcNote, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &src.ID, "keep", nil)
	require.NoError(t, err)
	_, err = st.svc.UpdateNoteContent(ctx, st.userID, srcNote.ID, "keep me", srcNote.ContentHash)
	require.NoError(t, err)

	// An unindexed directory exists on disk at the destination path with no DB
	// row, so the GetByDiskPath pre-check passes and only the no-replace rename
	// (renameat2 EEXIST) can stop the collision.
	orphanAbs := filepath.Join(st.notesDir, st.user.UUID, "DstDir")
	require.NoError(t, os.MkdirAll(orphanAbs, 0700))

	err = st.svc.RenameFolder(ctx, st.userID, st.user.UUID, src.ID, "DstDir")
	require.Error(t, err)
	assert.True(t, apperror.IsConflict(err), "disk-only destination must map to 409 Conflict, got %v", err)

	// Both the source tree and the unindexed destination directory must survive.
	srcAbs := filepath.Join(st.notesDir, src.DiskPath)
	for _, p := range []string{srcAbs, orphanAbs} {
		info, statErr := os.Stat(p)
		require.NoError(t, statErr, "directory must survive rejected rename: %s", p)
		assert.True(t, info.IsDir())
	}

	_, err = os.ReadFile(filepath.Join(st.notesDir, srcNote.DiskPath))
	require.NoError(t, err, "note inside source folder must survive rejected rename")
}
