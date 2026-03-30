package repository

import (
	"context"

	"github.com/th0rn0/thornotes/internal/model"
)

type UserRepository interface {
	Create(ctx context.Context, username, passwordHash string) (*model.User, error)
	GetByID(ctx context.Context, id int64) (*model.User, error)
	GetByUsername(ctx context.Context, username string) (*model.User, error)
	Count(ctx context.Context) (int, error)
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
	Tree(ctx context.Context, userID int64) ([]*model.FolderTreeItem, error)
	Rename(ctx context.Context, userID, folderID int64, newName, newDiskPath string) error
	// UpdateDescendantPaths is called as part of folder rename to cascade disk_path updates.
	// Must run inside the same transaction as the OS rename.
	UpdateDescendantPaths(ctx context.Context, oldPrefix, newPrefix string) error
	Delete(ctx context.Context, userID, folderID int64) error
}

type NoteRepository interface {
	Create(ctx context.Context, n *model.Note) (*model.Note, error)
	GetByID(ctx context.Context, userID, noteID int64) (*model.Note, error)
	GetByShareToken(ctx context.Context, token string) (*model.Note, error)
	ListByFolder(ctx context.Context, userID int64, folderID *int64) ([]*model.NoteListItem, error)
	Update(ctx context.Context, n *model.Note) error
	UpdateContent(ctx context.Context, userID, noteID int64, content, contentHash, expectedHash string) error
	Delete(ctx context.Context, userID, noteID int64) error
	SetShareToken(ctx context.Context, userID, noteID int64, token *string) error
}

type APITokenRepository interface {
	Create(ctx context.Context, userID int64, name, token string) (*model.APIToken, error)
	GetByToken(ctx context.Context, token string) (*model.APIToken, error)
	ListByUser(ctx context.Context, userID int64) ([]*model.APIToken, error)
	Delete(ctx context.Context, userID, tokenID int64) error
	TouchLastUsed(ctx context.Context, tokenID int64) error
}

type SearchRepository interface {
	// Search performs full-text search, syncing FTS for notes with stale fts_synced_at.
	Search(ctx context.Context, userID int64, query string, tags []string) ([]*model.SearchResult, error)
	// SyncNote explicitly brings a note's FTS entry up to date.
	SyncNote(ctx context.Context, noteID int64) error
}
