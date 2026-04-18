package repository

import (
	"context"

	"github.com/th0rn0/thornotes/internal/model"
)

type UserRepository interface {
	Create(ctx context.Context, username, passwordHash, uuid string) (*model.User, error)
	GetByID(ctx context.Context, id int64) (*model.User, error)
	GetByUsername(ctx context.Context, username string) (*model.User, error)
	Count(ctx context.Context) (int, error)
	IDs(ctx context.Context) ([]int64, error)
	// SetUUID assigns a UUID to an existing user (startup migration only).
	SetUUID(ctx context.Context, id int64, uuid string) error
	// ListWithoutUUID returns IDs of users that have no UUID assigned yet.
	ListWithoutUUID(ctx context.Context) ([]int64, error)
}

type SessionRepository interface {
	Create(ctx context.Context, token string, userID int64, ttlSeconds int) error
	Get(ctx context.Context, token string) (*model.Session, error)
	Delete(ctx context.Context, token string) error
	DeleteExpired(ctx context.Context) error
}

type FolderRepository interface {
	Create(ctx context.Context, userID int64, parentID *int64, name, diskPath string) (*model.Folder, error)
	GetByID(ctx context.Context, userID, folderID int64) (*model.Folder, error)
	// GetByDiskPath returns the folder with the given disk_path, or ErrNotFound.
	// disk_path is unique across all users so no userID filter is needed, but callers
	// should still verify ownership via the returned Folder.UserID.
	GetByDiskPath(ctx context.Context, diskPath string) (*model.Folder, error)
	Tree(ctx context.Context, userID int64) ([]*model.FolderTreeItem, error)
	Rename(ctx context.Context, userID, folderID int64, newName, newDiskPath string) error
	// Move updates the folder's parent_id and disk_path (used when reparenting via drag-and-drop).
	Move(ctx context.Context, userID, folderID int64, newParentID *int64, newDiskPath string) error
	// UpdateDescendantPaths is called as part of folder rename to cascade disk_path updates.
	// Must run inside the same transaction as the OS rename.
	UpdateDescendantPaths(ctx context.Context, oldPrefix, newPrefix string) error
	Delete(ctx context.Context, userID, folderID int64) error
}

type NoteRepository interface {
	Create(ctx context.Context, n *model.Note) (*model.Note, error)
	GetByID(ctx context.Context, userID, noteID int64) (*model.Note, error)
	GetByShareToken(ctx context.Context, token string) (*model.Note, error)
	// GetByFolderAndSlug returns a note by folder + slug, or ErrNotFound.
	// Pass nil folderID to look up root notes.
	GetByFolderAndSlug(ctx context.Context, userID int64, folderID *int64, slug string) (*model.Note, error)
	ListByFolder(ctx context.Context, userID int64, folderID *int64) ([]*model.NoteListItem, error)
	// ListAll returns note metadata for all notes owned by userID, across all folders.
	ListAll(ctx context.Context, userID int64) ([]*model.NoteListItem, error)
	// ListAllForWatch returns lightweight records for all notes owned by userID.
	// Used by the disk watcher to detect content changes without loading full content.
	ListAllForWatch(ctx context.Context, userID int64) ([]*model.NoteWatchRecord, error)
	// ListForContext returns notes with full content for the LLM context endpoint.
	// Pass nil folderID to include all notes across all folders.
	// Results are ordered newest-first (updated_at DESC) for truncation priority.
	ListForContext(ctx context.Context, userID int64, folderID *int64) ([]*model.Note, error)
	Update(ctx context.Context, n *model.Note) error
	UpdateContent(ctx context.Context, userID, noteID int64, content, contentHash, expectedHash string) error
	// Move updates the note's folder_id and disk_path (used when reparenting via drag-and-drop).
	Move(ctx context.Context, userID, noteID int64, newFolderID *int64, newDiskPath string) error
	Delete(ctx context.Context, userID, noteID int64) error
	SetShareToken(ctx context.Context, userID, noteID int64, token *string) error
}

type APITokenRepository interface {
	Create(ctx context.Context, userID int64, name, token, scope string) (*model.APIToken, error)
	GetByToken(ctx context.Context, token string) (*model.APIToken, error)
	ListByUser(ctx context.Context, userID int64) ([]*model.APIToken, error)
	Delete(ctx context.Context, userID, tokenID int64) error
	TouchLastUsed(ctx context.Context, tokenID int64) error
	// ListPermissions returns the per-folder permissions for a token. An empty
	// slice means the token operates under its global Scope.
	ListPermissions(ctx context.Context, tokenID int64) ([]model.TokenFolderPermission, error)
	// SetPermissions replaces the full set of folder permissions for a token.
	// It validates that every non-nil folder_id belongs to userID. Passing an
	// empty slice removes all permissions (reverts the token to global scope).
	SetPermissions(ctx context.Context, userID, tokenID int64, perms []model.TokenFolderPermission) error
}

type JournalRepository interface {
	Create(ctx context.Context, userID int64, name string) (*model.Journal, error)
	GetByID(ctx context.Context, userID, journalID int64) (*model.Journal, error)
	ListByUser(ctx context.Context, userID int64) ([]*model.Journal, error)
	Delete(ctx context.Context, userID, journalID int64) error
}

type SearchRepository interface {
	// Search performs full-text search, syncing FTS for notes with stale fts_synced_at.
	Search(ctx context.Context, userID int64, query string, tags []string) ([]*model.SearchResult, error)
	// SyncNote explicitly brings a note's FTS entry up to date.
	SyncNote(ctx context.Context, noteID int64) error
}
