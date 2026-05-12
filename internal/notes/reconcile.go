package notes

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

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
	diskFolders, diskNotes, err := s.walkUserTree(user.UUID)
	if err != nil {
		return result, err
	}

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

	// 5. Delete DB notes still missing on disk after repair. Do this before
	//    folder delete so child notes are gone first (folder.notes.folder_id
	//    is ON DELETE SET NULL otherwise).
	for diskPath, n := range dbNotesByPath {
		if _, ok := diskNotes[diskPath]; ok {
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
	missingFolderPaths := make([]string, 0)
	for p, f := range dbFoldersByPath {
		if _, ok := diskFolders[p]; ok {
			continue
		}
		_ = f
		missingFolderPaths = append(missingFolderPaths, p)
	}
	sort.Slice(missingFolderPaths, func(i, j int) bool {
		return pathDepth(missingFolderPaths[i]) > pathDepth(missingFolderPaths[j])
	})
	for _, p := range missingFolderPaths {
		f := dbFoldersByPath[p]
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

// walkUserTree returns:
//   - the set of folder disk_paths found under the user's UUID directory
//     (relative to notesRoot),
//   - a map of note disk_path → file content for every .md file.
//
// Hidden directories (anything starting with ".") and the FileStore's own
// ".thornotes-*.tmp" write-temps are skipped. A missing user directory is
// not an error: an empty result lets the reconciler delete every DB row for
// the user and converge to the empty truth on disk.
func (s *Service) walkUserTree(userUUID string) (map[string]struct{}, map[string]string, error) {
	folders := map[string]struct{}{}
	notes := map[string]string{}
	rootAbs, err := s.fs.safePath(userUUID)
	if err != nil {
		return folders, notes, err
	}
	walkErr := filepath.WalkDir(rootAbs, func(absPath string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return nil
		}
		if absPath == rootAbs {
			return nil
		}
		rel, relErr := filepath.Rel(s.fs.notesRoot, absPath)
		if relErr != nil {
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
			folders[rel] = struct{}{}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			return nil
		}
		content, readErr := os.ReadFile(absPath)
		if readErr != nil {
			log.Warn().Err(readErr).Str("path", rel).Msg("reconcile: read file")
			return nil
		}
		notes[rel] = string(content)
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return folders, notes, walkErr
	}
	return folders, notes, nil
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
