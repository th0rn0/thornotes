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
	// ListSubtree returns diskPath's own folder row and every folder beneath it,
	// using DescendantPathPredicate (ticket 598) so the match is bounded at a path
	// separator and LIKE metacharacters in a name cannot widen it. One query, not
	// a walk of the whole tree.
	ListSubtree(ctx context.Context, userID int64, diskPath string) ([]*model.Folder, error)
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
	// ListSubtree returns metadata for every note whose disk_path lies inside the
	// folder subtree at diskPath (the folder itself included, so notes filed
	// directly in it match), ordered by disk_path. It uses the same
	// DescendantPathPredicate as the folder subtree query, so both halves of the
	// export agree on what "under this folder" means.
	ListSubtree(ctx context.Context, userID int64, diskPath string) ([]*model.NoteExportRecord, error)
	// ListForContext returns notes with full content for the LLM context endpoint.
	// Pass nil folderID to include all notes across all folders.
	// Results are ordered newest-first (updated_at DESC) for truncation priority.
	ListForContext(ctx context.Context, userID int64, folderID *int64) ([]*model.Note, error)
	Update(ctx context.Context, n *model.Note) error
	// UpdateMetadataAndContent writes the note's metadata AND its body in one
	// statement. UpdateNoteMetadata needs that: a title change rewrites the
	// file's "# H1", so the row's title, slug, path and body all move together,
	// and two statements would leave a window in which the row's title and its
	// content disagree — the divergence ticket 467 is about.
	UpdateMetadataAndContent(ctx context.Context, n *model.Note) error
	UpdateContent(ctx context.Context, userID, noteID int64, content, contentHash, expectedHash string) error
	// SetTitle repairs a note's title and its normalized title key without
	// touching slug, disk_path, content or tags. It exists for the reconciler,
	// which learns a note's title from the file's "# H1" and has no full row to
	// hand Update (Update would rewrite tags it never loaded).
	SetTitle(ctx context.Context, userID, noteID int64, title, titleKey string) error
	// SetTitleKey updates a note's normalized title key (link-resolution index,
	// backfilled on reconcile).
	SetTitleKey(ctx context.Context, userID, noteID int64, titleKey string) error
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
	// SetScope updates the token's global read/write scope. Caller must pass
	// "read" or "readwrite"; the repo enforces ownership.
	SetScope(ctx context.Context, userID, tokenID int64, scope string) error
	// SetName updates the display name of a token. The name is metadata only;
	// the token value and hash are untouched. Empty names are rejected.
	SetName(ctx context.Context, userID, tokenID int64, name string) error
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

// LinkRepository persists and queries the resolved link graph (note_links).
type LinkRepository interface {
	// ReplaceLinks atomically replaces all outgoing edges of fromNoteID with
	// edges (delete-then-insert). Callers hold the per-note lock.
	ReplaceLinks(ctx context.Context, userID, fromNoteID int64, edges []model.NoteLink) error
	// DeleteForUser removes every note_links row owned by userID (full-reindex prep).
	DeleteForUser(ctx context.Context, userID int64) error
	// ListOutgoing returns the outgoing edges of a note.
	ListOutgoing(ctx context.Context, userID, noteID int64) ([]model.NoteLink, error)
	// ListBacklinks returns inbound edges (to_note_id = noteID) enriched with
	// the source note's title and folder.
	ListBacklinks(ctx context.Context, userID, noteID int64) ([]model.Backlink, error)
	// ListByTargetHash returns edges whose target_hash matches (edge_key lookup).
	ListByTargetHash(ctx context.Context, userID int64, targetHash string) ([]model.NoteLink, error)
	// ListInboundForRename returns inbound edges (to_note_id = noteID) with the
	// source note's folder, for re-resolution after a title rename.
	ListInboundForRename(ctx context.Context, userID, noteID int64) ([]model.InboundLink, error)
	// UpdateResolution updates an edge's to_note_id, ambiguous flag and
	// resolved_at (rename re-resolution). It returns ErrNotFound when no edge
	// with this id belongs to userID — the row is gone (a concurrent reindex
	// replaces the graph) or was never theirs — because reporting success there
	// would claim a link had been re-resolved while its row still pointed at
	// the old target. A same-value update is success on both dialects.
	UpdateResolution(ctx context.Context, userID, linkID int64, toNoteID *int64, ambiguous bool) error
}
