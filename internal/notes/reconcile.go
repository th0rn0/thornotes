package notes

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// diskTree is one user's on-disk state as observed by a single walk, together
// with what the walk could not observe.
//
// The distinction that matters is absence versus ignorance. `folders` and
// `notes` are only populated for paths whose state was positively determined;
// `unknown` records the paths whose state could not be (a stat/read error such
// as EACCES or EIO, a directory that could not be listed, or a non-regular file
// we refuse to treat as a note). A DB row may only be deleted when the disk
// says its file is *gone* — never when the disk merely failed to answer.
type diskTree struct {
	folders map[string]struct{}
	notes   map[string]string
	unknown []string
	root    rootState
}

// rootState is what the walk learned about the user's own directory.
type rootState struct {
	exists   bool
	readable bool
	empty    bool
}

// isUnknown reports whether rel is, or is underneath, a path whose state could
// not be determined.
func (t diskTree) isUnknown(rel string) bool {
	for _, u := range t.unknown {
		if rel == u || strings.HasPrefix(rel, u+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// reconcileResult summarises what the reconciler did for a user. It is the
// return value of reconcileUserFromDisk and the input to the watcher's
// "should I notify the SSE hub?" decision.
type reconcileResult struct {
	NotesCreated      int
	NotesDeleted      int
	NotesUpdated      int
	NotesPathRepaired int
	FoldersCreated    int
	FoldersDeleted    int
	FoldersRepaired   int
}

// total returns the total number of disk-vs-DB drift events repaired.
func (r reconcileResult) total() int {
	return r.NotesCreated + r.NotesDeleted + r.NotesUpdated + r.NotesPathRepaired +
		r.FoldersCreated + r.FoldersDeleted + r.FoldersRepaired
}

// reconcileUserFromDisk performs full bidirectional sync between the on-disk
// note tree and the database for a single user.
//
// File-first semantics: disk is the source of truth, the DB is the index.
//
//   - Folders on disk not in DB → INSERT folder row.
//   - Notes on disk not in DB → INSERT note row (title derived from first
//     heading or filename).
//   - DB rows whose disk_path is missing on disk → first attempt to repair
//     (e.g. parent folder was renamed and the descendant cascade failed),
//     then DELETE if no repair is possible.
//   - Matched pairs → hash compare; the disk wins on mismatch.
//
// It returns the count of changes applied, plus any fatal error (transient
// per-row errors are logged and skipped).
func (s *Service) reconcileUserFromDisk(ctx context.Context, user *model.User) (reconcileResult, error) {
	var result reconcileResult
	if user.UUID == "" {
		return result, nil
	}

	// 1. Walk disk.
	tree, err := s.walkUserTree(user.UUID)
	if err != nil {
		return result, err
	}
	diskFolders, diskNotes := tree.folders, tree.notes

	// 2. Load DB state.
	dbFolders, err := s.folders.Tree(ctx, user.ID)
	if err != nil {
		return result, err
	}
	dbNotes, err := s.notes.ListAllForWatch(ctx, user.ID)
	if err != nil {
		return result, err
	}

	dbFoldersByPath := make(map[string]*model.FolderTreeItem, len(dbFolders))
	dbFoldersByID := make(map[int64]*model.FolderTreeItem, len(dbFolders))
	for _, f := range dbFolders {
		dbFoldersByPath[f.DiskPath] = f
		dbFoldersByID[f.ID] = f
	}
	dbNotesByPath := make(map[string]*model.NoteWatchRecord, len(dbNotes))
	for _, n := range dbNotes {
		dbNotesByPath[n.DiskPath] = n
	}

	// 2b. Mount precondition. A missing or unreadable notes root is not a user
	//     with no notes: it is an instance that cannot see its own storage. If
	//     the index holds rows, refuse the pass outright rather than converging
	//     to a "truth" that would delete every note, tag and share token while
	//     the files sit intact on a volume that simply is not mounted.
	if err := s.checkRootPrecondition(user, tree, len(dbNotes), len(dbFolders)); err != nil {
		logAbort(user.ID, s.fs.notesRoot, err)
		return result, err
	}

	// 3. Repair stale folder disk_paths. A folder is "stale" when its DB
	//    disk_path is gone from disk but a directory matching its name now
	//    exists under its parent's current disk_path. This is the
	//    folder-rename-cascade-failure recovery path.
	for _, f := range dbFolders {
		if _, ok := diskFolders[f.DiskPath]; ok {
			continue
		}
		parentPath := user.UUID
		if f.ParentID != nil {
			parent, ok := dbFoldersByID[*f.ParentID]
			if !ok {
				continue
			}
			parentPath = parent.DiskPath
		}
		expectedPath := filepath.Join(parentPath, f.Name)
		if _, ok := diskFolders[expectedPath]; !ok {
			continue
		}
		if _, taken := dbFoldersByPath[expectedPath]; taken {
			continue
		}
		if err := s.folders.Rename(ctx, user.ID, f.ID, f.Name, expectedPath); err != nil {
			log.Warn().Err(err).Int64("folder_id", f.ID).Str("from", f.DiskPath).Str("to", expectedPath).Msg("reconcile: repair folder disk_path")
			continue
		}
		delete(dbFoldersByPath, f.DiskPath)
		f.DiskPath = expectedPath
		dbFoldersByPath[expectedPath] = f
		result.FoldersRepaired++
	}

	// 4. Repair stale note disk_paths (same shape as step 3 but for notes).
	for _, n := range dbNotes {
		if _, ok := diskNotes[n.DiskPath]; ok {
			continue
		}
		parentPath := user.UUID
		if n.FolderID != nil {
			parent, ok := dbFoldersByID[*n.FolderID]
			if !ok {
				continue
			}
			parentPath = parent.DiskPath
		}
		expectedPath := filepath.Join(parentPath, n.Slug+".md")
		if _, ok := diskNotes[expectedPath]; !ok {
			continue
		}
		if _, taken := dbNotesByPath[expectedPath]; taken {
			continue
		}
		if err := s.notes.Move(ctx, user.ID, n.ID, n.FolderID, expectedPath); err != nil {
			log.Warn().Err(err).Int64("note_id", n.ID).Str("from", n.DiskPath).Str("to", expectedPath).Msg("reconcile: repair note disk_path")
			continue
		}
		delete(dbNotesByPath, n.DiskPath)
		n.DiskPath = expectedPath
		dbNotesByPath[expectedPath] = n
		result.NotesPathRepaired++
	}

	// 5. Decide what to delete, then guard the decision before applying it.
	//    A row is only deletable when the disk positively reports its path
	//    gone; a path the walk could not read is unknown, and unknown is never
	//    absent.
	noteDeletePaths := make([]string, 0, len(dbNotesByPath))
	for diskPath := range dbNotesByPath {
		if _, ok := diskNotes[diskPath]; ok {
			continue
		}
		if tree.isUnknown(diskPath) {
			log.Warn().Int64("note_id", dbNotesByPath[diskPath].ID).Str("disk_path", diskPath).
				Msg("reconcile: note path unreadable or unverifiable — keeping DB row")
			continue
		}
		noteDeletePaths = append(noteDeletePaths, diskPath)
	}
	folderDeletePaths := make([]string, 0, len(dbFoldersByPath))
	for p := range dbFoldersByPath {
		if _, ok := diskFolders[p]; ok {
			continue
		}
		if tree.isUnknown(p) {
			log.Warn().Int64("folder_id", dbFoldersByPath[p].ID).Str("disk_path", p).
				Msg("reconcile: folder path unreadable or unverifiable — keeping DB row")
			continue
		}
		folderDeletePaths = append(folderDeletePaths, p)
	}
	if err := s.checkDeletionBudget(user, len(dbNotes), noteDeletePaths); err != nil {
		logAbort(user.ID, s.fs.notesRoot, err)
		return result, err
	}

	// 5b. Delete DB notes whose files are gone. Before folder delete so child
	//     notes are gone first (folder.notes.folder_id is ON DELETE SET NULL
	//     otherwise).
	for _, diskPath := range noteDeletePaths {
		n := dbNotesByPath[diskPath]
		if n == nil {
			continue
		}
		if err := s.notes.Delete(ctx, user.ID, n.ID); err != nil {
			log.Warn().Err(err).Int64("note_id", n.ID).Str("disk_path", diskPath).Msg("reconcile: delete missing note")
			continue
		}
		delete(dbNotesByPath, diskPath)
		result.NotesDeleted++
	}

	// 6. Delete DB folders still missing on disk after repair. Walk deepest
	//    first so children are gone before parents.
	sort.Slice(folderDeletePaths, func(i, j int) bool {
		return pathDepth(folderDeletePaths[i]) > pathDepth(folderDeletePaths[j])
	})
	for _, p := range folderDeletePaths {
		f := dbFoldersByPath[p]
		if f == nil {
			continue
		}
		if err := s.folders.Delete(ctx, user.ID, f.ID); err != nil {
			log.Warn().Err(err).Int64("folder_id", f.ID).Str("disk_path", p).Msg("reconcile: delete missing folder")
			continue
		}
		delete(dbFoldersByPath, p)
		delete(dbFoldersByID, f.ID)
		result.FoldersDeleted++
	}

	// 7. Insert disk folders missing from DB. Walk shallowest first so the
	//    parent row exists before any child tries to reference it.
	newFolderPaths := make([]string, 0)
	for p := range diskFolders {
		if _, ok := dbFoldersByPath[p]; ok {
			continue
		}
		newFolderPaths = append(newFolderPaths, p)
	}
	sort.Slice(newFolderPaths, func(i, j int) bool {
		return pathDepth(newFolderPaths[i]) < pathDepth(newFolderPaths[j])
	})
	for _, p := range newFolderPaths {
		parentPath := filepath.Dir(p)
		var parentID *int64
		if parentPath != user.UUID && parentPath != "." {
			parent, ok := dbFoldersByPath[parentPath]
			if !ok {
				log.Warn().Str("disk_path", p).Msg("reconcile: skip folder with missing parent")
				continue
			}
			parentID = &parent.ID
		}
		name := filepath.Base(p)
		f, err := s.folders.Create(ctx, user.ID, parentID, name, p)
		if err != nil {
			log.Warn().Err(err).Str("disk_path", p).Msg("reconcile: insert folder")
			continue
		}
		item := &model.FolderTreeItem{ID: f.ID, ParentID: parentID, Name: name, DiskPath: p}
		dbFoldersByPath[p] = item
		dbFoldersByID[f.ID] = item
		result.FoldersCreated++
	}

	// 8. Insert disk notes missing from DB.
	for diskPath, content := range diskNotes {
		if _, ok := dbNotesByPath[diskPath]; ok {
			continue
		}
		parentDir := filepath.Dir(diskPath)
		var folderID *int64
		if parentDir != user.UUID && parentDir != "." {
			parent, ok := dbFoldersByPath[parentDir]
			if !ok {
				log.Warn().Str("disk_path", diskPath).Msg("reconcile: skip note with missing parent folder")
				continue
			}
			folderID = &parent.ID
		}
		slug := strings.TrimSuffix(filepath.Base(diskPath), ".md")
		title := deriveTitle(content, slug)
		hash := HashContent(content)
		n := &model.Note{
			UserID:      user.ID,
			FolderID:    folderID,
			Title:       title,
			Slug:        slug,
			DiskPath:    diskPath,
			Content:     content,
			ContentHash: hash,
			Tags:        []string{},
		}
		created, err := s.notes.Create(ctx, n)
		if err != nil {
			if apperror.IsConflict(err) {
				continue
			}
			log.Warn().Err(err).Str("disk_path", diskPath).Msg("reconcile: insert note")
			continue
		}
		dbNotesByPath[diskPath] = &model.NoteWatchRecord{
			ID: created.ID, FolderID: folderID, Slug: slug, DiskPath: diskPath, ContentHash: hash,
		}
		result.NotesCreated++
	}

	// 9. Hash-compare matched pairs. The disk wins on mismatch.
	for diskPath, content := range diskNotes {
		rec, ok := dbNotesByPath[diskPath]
		if !ok {
			continue
		}
		hash := HashContent(content)
		if hash == rec.ContentHash {
			continue
		}
		mu := s.lockNote(rec.ID)
		mu.Lock()
		if err := s.notes.UpdateContent(ctx, user.ID, rec.ID, content, hash, rec.ContentHash); err != nil {
			mu.Unlock()
			log.Warn().Err(err).Int64("note_id", rec.ID).Str("disk_path", diskPath).Msg("reconcile: update content")
			continue
		}
		mu.Unlock()
		rec.ContentHash = hash
		result.NotesUpdated++
	}

	return result, nil
}

// checkRootPrecondition implements the C-03 mount precondition: a notes root
// that cannot be seen must never be read as a user who has no notes.
func (s *Service) checkRootPrecondition(user *model.User, tree diskTree, dbNoteCount, dbFolderCount int) error {
	if dbNoteCount == 0 && dbFolderCount == 0 {
		return nil // nothing to lose, and nothing to be wrong about
	}
	if !tree.root.exists {
		return fmt.Errorf(
			"%w: notes root %q for user %d is missing, but the index holds %d notes and %d folders — a missing mount is not an empty one; restore the mount and reconcile again",
			ErrReconcileAborted, s.fs.notesRoot, user.ID, dbNoteCount, dbFolderCount)
	}
	if !tree.root.readable {
		return fmt.Errorf(
			"%w: notes root %q for user %d is unreadable, but the index holds %d notes and %d folders — unreadable is unknown, not absent",
			ErrReconcileAborted, s.fs.notesRoot, user.ID, dbNoteCount, dbFolderCount)
	}
	// An empty-but-present root is ambiguous: it is the signature of a mount
	// that came up empty, and also of a user who deleted their last note. Only
	// the second is legitimate, and it cannot plausibly involve a burst of rows,
	// so the same floor the circuit breaker uses settles it.
	if dbNoteCount >= s.reconcileMinDeleteTrips && tree.root.empty {
		return fmt.Errorf(
			"%w: notes root %q for user %d is empty, but the index holds %d notes — refusing to converge a populated index to an empty directory",
			ErrReconcileAborted, s.fs.notesRoot, user.ID, dbNoteCount)
	}
	return nil
}

// checkDeletionBudget is the deletion circuit breaker. The mount precondition
// catches a wholly absent root; this catches the partial disasters — half a
// tree unmounted, an rsync that dropped a directory, a chmod that hid one
// subtree — where the pass would delete a large minority rather than all of it.
func (s *Service) checkDeletionBudget(user *model.User, totalNotes int, noteDeletes []string) error {
	if len(noteDeletes) == 0 || totalNotes == 0 {
		return nil
	}
	if len(noteDeletes) < s.reconcileMinDeleteTrips {
		return nil // ordinary small edits (and small accounts) reconcile normally
	}
	limit := int(math.Floor(s.reconcileMaxDeleteRatio * float64(totalNotes)))
	if len(noteDeletes) <= limit {
		return nil
	}
	pct := 100 * float64(len(noteDeletes)) / float64(totalNotes)
	sort.Strings(noteDeletes) // deterministic logs
	log.Error().
		Int64("user_id", user.ID).
		Int("would_delete", len(noteDeletes)).
		Int("total_notes", totalNotes).
		Float64("percent", math.Round(pct)).
		Float64("configured_max_percent", s.reconcileMaxDeleteRatio*100).
		Strs("would_delete_paths", noteDeletes).
		Msg("reconcile: deletion circuit breaker tripped — these rows would have been deleted")
	return fmt.Errorf(
		"%w: this pass would delete %d of %d notes (%.0f%%, over the configured %.0f%% guard); nothing was deleted — check the volume before reconciling",
		ErrReconcileAborted, len(noteDeletes), totalNotes, pct, s.reconcileMaxDeleteRatio*100)
}

// walkUserTree returns the user's on-disk folders and note contents, plus the
// paths whose state could not be determined.
//
// Hidden directories (anything starting with ".") and the FileStore's own
// ".thornotes-*.tmp" write-temps are skipped. A missing user directory yields
// an empty tree with root.exists = false; the caller decides what that means,
// because a missing directory is a data-loss signal whenever the index is not
// empty. Read errors never produce "absent": they produce an unknown path that
// the reconciler refuses to delete against.
func (s *Service) walkUserTree(userUUID string) (diskTree, error) {
	tree := diskTree{folders: map[string]struct{}{}, notes: map[string]string{}}
	rootAbs, err := s.fs.safePath(userUUID)
	if err != nil {
		return tree, err
	}

	// Probe the root explicitly: WalkDir reports a stat failure for the root as
	// a plain error, and we need to distinguish "not there" from "cannot see".
	info, statErr := os.Stat(rootAbs)
	switch {
	case statErr != nil && errors.Is(statErr, fs.ErrNotExist):
		return tree, nil // absent — checkRootPrecondition decides if that is fatal
	case statErr != nil:
		tree.root = rootState{exists: true, readable: false}
		tree.unknown = append(tree.unknown, userUUID)
		log.Error().Err(statErr).Str("root", rootAbs).Msg("reconcile: notes root cannot be stat'ed — treating as unknown")
		return tree, nil
	case !info.IsDir():
		tree.root = rootState{exists: true, readable: false}
		tree.unknown = append(tree.unknown, userUUID)
		log.Error().Str("root", rootAbs).Msg("reconcile: notes root is not a directory — treating as unknown")
		return tree, nil
	}

	entries, readErr := os.ReadDir(rootAbs)
	if readErr != nil {
		tree.root = rootState{exists: true, readable: false}
		tree.unknown = append(tree.unknown, userUUID)
		log.Error().Err(readErr).Str("root", rootAbs).Msg("reconcile: notes root cannot be read — treating as unknown")
		return tree, nil
	}
	tree.root = rootState{exists: true, readable: true, empty: len(entries) == 0}

	walkErr := filepath.WalkDir(rootAbs, func(absPath string, d fs.DirEntry, err error) error {
		if absPath == rootAbs {
			if err != nil {
				// The root we just listed has become unreadable.
				tree.root.readable = false
				tree.unknown = append(tree.unknown, userUUID)
				return filepath.SkipAll
			}
			return nil
		}
		rel, relErr := filepath.Rel(s.fs.notesRoot, absPath)
		if relErr != nil {
			return nil
		}
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Genuinely gone (deleted between listing and stat): absent.
				return nil
			}
			// EACCES, EIO, ELOOP, ENOTDIR …: we cannot know what is here.
			tree.unknown = append(tree.unknown, rel)
			log.Error().Err(err).Str("path", rel).Msg("reconcile: path unreadable — treating as unknown, rows under it are protected")
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			tree.folders[rel] = struct{}{}
			return nil
		}
		if !d.Type().IsRegular() {
			// Symlink, socket, fifo, device: not a note we own, and not
			// evidence that any row's file is gone.
			tree.unknown = append(tree.unknown, rel)
			log.Warn().Str("path", rel).Msg("reconcile: non-regular file skipped — rows under it are protected")
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			return nil
		}
		content, readErr := os.ReadFile(absPath)
		if readErr != nil {
			tree.unknown = append(tree.unknown, rel)
			log.Error().Err(readErr).Str("path", rel).Msg("reconcile: note unreadable — treating as unknown, DB row protected")
			return nil
		}
		tree.notes[rel] = string(content)
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
		return tree, walkErr
	}
	return tree, nil
}

// deriveTitle returns the first markdown H1 ("# Heading") line in content,
// or fallback if none exists. The slug is taken from the filename, so the
// title is the human-readable label the UI shows in the tree.
func deriveTitle(content, fallback string) string {
	for _, line := range strings.SplitN(content, "\n", 16) {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "# ") {
			continue
		}
		title := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		if title == "" {
			continue
		}
		if len(title) > 500 {
			title = title[:500]
		}
		return title
	}
	return fallback
}

// pathDepth counts the number of path separators in p. Used to order folder
// inserts (shallowest first, so parents exist before children) and deletes
// (deepest first, so children are gone before parents).
func pathDepth(p string) int {
	return strings.Count(p, string(filepath.Separator))
}
