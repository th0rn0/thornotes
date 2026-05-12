package notes

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
)

// noteLockStripes bounds memory for per-note serialisation. A note's ID is
// mapped to one of N mutexes; same ID always lands on the same mutex, different
// IDs may collide but the contention is bounded and harmless.
const noteLockStripes = 64

// Service coordinates note and folder operations across the FileStore and repositories.
type Service struct {
	notes      repository.NoteRepository
	folders    repository.FolderRepository
	search     repository.SearchRepository
	journals   repository.JournalRepository
	fs         *FileStore
	noteLocks  [noteLockStripes]sync.Mutex
}

func NewService(
	notes repository.NoteRepository,
	folders repository.FolderRepository,
	search repository.SearchRepository,
	journals repository.JournalRepository,
	fs *FileStore,
) *Service {
	return &Service{
		notes:    notes,
		folders:  folders,
		search:   search,
		journals: journals,
		fs:       fs,
	}
}

// lockNote returns the striped mutex for noteID. Callers must Lock/Unlock.
// Held across the (file write + DB write) pair so concurrent saves and the
// disk watcher cannot interleave on the same note.
func (s *Service) lockNote(noteID int64) *sync.Mutex {
	if noteID < 0 {
		noteID = -noteID
	}
	return &s.noteLocks[noteID%noteLockStripes]
}

// HashContent returns the SHA-256 hex digest of content.
func HashContent(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

// Reconcile performs a full bidirectional sync between disk and DB for a
// single user. The on-disk note tree is the source of truth: notes/folders
// that exist on disk but not in the DB are inserted, DB rows whose files
// have disappeared are deleted (after a repair attempt that handles the
// folder-rename cascade-failure case), and content drift is hash-compared
// in the disk-wins direction.
//
// This runs at startup, and is also the implementation behind the polling
// disk watcher. Callers pass a fully populated *model.User so that the
// reconciler can scope its filesystem walk to the user's UUID directory.
// See reconcile.go for the algorithm.
func (s *Service) Reconcile(ctx context.Context, user *model.User) (int, error) {
	if user == nil {
		return 0, fmt.Errorf("reconcile: user is nil")
	}
	log.Info().Int64("user_id", user.ID).Msg("reconcile: starting")
	result, err := s.reconcileUserFromDisk(ctx, user)
	if err != nil {
		return 0, err
	}
	if result.total() > 0 {
		log.Info().
			Int64("user_id", user.ID).
			Int("notes_created", result.NotesCreated).
			Int("notes_deleted", result.NotesDeleted).
			Int("notes_updated", result.NotesUpdated).
			Int("notes_repaired", result.NotesPathRepaired).
			Int("folders_created", result.FoldersCreated).
			Int("folders_deleted", result.FoldersDeleted).
			Int("folders_repaired", result.FoldersRepaired).
			Msg("reconcile: changes applied")
	}
	log.Info().Int64("user_id", user.ID).Int("total_changes", result.total()).Msg("reconcile: complete")
	return result.total(), nil
}

// notesDiskPath returns the relative disk path for a note.
// e.g. "{userUUID}/Work/my-note.md"
func notesDiskPath(userUUID string, folderDiskPath, slug string) string {
	if folderDiskPath == "" {
		return filepath.Join(userUUID, slug+".md")
	}
	return filepath.Join(folderDiskPath, slug+".md")
}

// folderDiskPath returns the relative disk path for a folder.
func folderDiskPath(userUUID string, parentDiskPath, name string) string {
	if parentDiskPath == "" {
		return filepath.Join(userUUID, name)
	}
	return filepath.Join(parentDiskPath, name)
}

// slugify converts a title to a safe filename slug.
func slugify(title string) string {
	slug := ""
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z':
			slug += string(r)
		case r >= 'A' && r <= 'Z':
			slug += string(r + 32) // toLower
		case r >= '0' && r <= '9':
			slug += string(r)
		case r == ' ' || r == '-' || r == '_':
			if len(slug) > 0 && slug[len(slug)-1] != '-' {
				slug += "-"
			}
		}
	}
	// Trim trailing dash.
	for len(slug) > 0 && slug[len(slug)-1] == '-' {
		slug = slug[:len(slug)-1]
	}
	if slug == "" {
		slug = "untitled"
	}
	if len(slug) > 100 {
		slug = slug[:100]
	}
	return slug
}

// FileStore returns the underlying FileStore. Used by tests and startup code
// to enable optional features like git history.
func (s *Service) FileStore() *FileStore {
	return s.fs
}

// Folders returns the FolderRepository. Used by the MCP handler to walk
// folder ancestor chains when resolving per-token permissions.
func (s *Service) Folders() repository.FolderRepository {
	return s.folders
}

// ptrEq reports whether two *int64 pointers point to equal values (or are both nil).
func ptrEq(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// FolderTree returns the folder tree for a user.
func (s *Service) FolderTree(ctx context.Context, userID int64) ([]*model.FolderTreeItem, error) {
	return s.folders.Tree(ctx, userID)
}
