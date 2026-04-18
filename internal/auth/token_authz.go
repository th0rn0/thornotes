package auth

import (
	"context"
	"errors"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
)

// TokenAuthz answers "can this API token read/write this folder?".
//
// A token can be in one of two modes:
//
//   - Global mode: no per-folder permissions configured. The token's Scope
//     ("read" or "readwrite") applies to every folder, matching the legacy
//     behavior before fine-grained permissions existed.
//
//   - Scoped mode: one or more rows in api_token_folder_permissions. Access
//     is granted only to listed folders (and their descendants, via
//     parent-chain walk). A row with folder_id = NULL grants access to the
//     root (unfiled) area. When a folder's own row and an ancestor's row
//     both exist, the closest one wins, so a "read" grant on a child can
//     sit underneath a "write" grant on the parent.
type TokenAuthz struct {
	// Scope is the token's global fallback: "read" or "readwrite". Used
	// only when Scoped is false.
	Scope string
	// Scoped is true when per-folder permissions are in effect.
	Scoped bool
	// byFolder maps folder_id → permission ("read"|"write"). Only set when
	// Scoped is true.
	byFolder map[int64]string
	// rootPerm is the permission assigned to the root area (folder_id = NULL),
	// or "" when no such row exists. Only consulted when Scoped is true.
	rootPerm string
}

// SessionAuthz returns a TokenAuthz that grants full read/write access on
// every folder. Used for requests authenticated by session cookie (UI),
// which have no concept of API-token scoping.
func SessionAuthz() *TokenAuthz {
	return &TokenAuthz{Scope: "readwrite"}
}

// NewTokenAuthz builds a TokenAuthz from the raw token record and its
// permission rows.
func NewTokenAuthz(token *model.APIToken, perms []model.TokenFolderPermission) *TokenAuthz {
	a := &TokenAuthz{Scope: token.Scope}
	if len(perms) == 0 {
		return a
	}
	a.Scoped = true
	a.byFolder = make(map[int64]string, len(perms))
	for _, p := range perms {
		if p.FolderID == nil {
			a.rootPerm = p.Permission
			continue
		}
		a.byFolder[*p.FolderID] = p.Permission
	}
	return a
}

// CanRead reports whether the token may read inside the given folder.
// folderID is nil for root-level items.
func (a *TokenAuthz) CanRead(ctx context.Context, folders repository.FolderRepository, userID int64, folderID *int64) (bool, error) {
	if a == nil {
		return false, nil
	}
	if !a.Scoped {
		// Both "read" and "readwrite" can read.
		return a.Scope == "read" || a.Scope == "readwrite", nil
	}
	perm, err := a.resolve(ctx, folders, userID, folderID)
	if err != nil {
		return false, err
	}
	// Any grant ("read" or "write") implies read.
	return perm == "read" || perm == "write", nil
}

// CanWrite reports whether the token may write inside the given folder.
// folderID is nil for root-level items.
func (a *TokenAuthz) CanWrite(ctx context.Context, folders repository.FolderRepository, userID int64, folderID *int64) (bool, error) {
	if a == nil {
		return false, nil
	}
	if !a.Scoped {
		return a.Scope == "readwrite", nil
	}
	perm, err := a.resolve(ctx, folders, userID, folderID)
	if err != nil {
		return false, err
	}
	return perm == "write", nil
}

// HasAnyAccess reports whether the token has at least read access to any
// folder. Used to short-circuit when listing the full folder tree.
func (a *TokenAuthz) HasAnyAccess() bool {
	if a == nil {
		return false
	}
	if !a.Scoped {
		return a.Scope == "read" || a.Scope == "readwrite"
	}
	return a.rootPerm != "" || len(a.byFolder) > 0
}

// FilterReadableFolderIDs returns a map keyed by folder ID for every folder
// in the passed tree the token can read. The root ("" sentinel) is not
// included — check rootReadable separately.
//
// When the token is not scoped (global readwrite/read), every folder is
// returned as readable and rootReadable is true.
func (a *TokenAuthz) FilterReadableFolderIDs(tree []*model.FolderTreeItem) (readable map[int64]bool, rootReadable bool) {
	readable = make(map[int64]bool, len(tree))
	if a == nil {
		return readable, false
	}
	if !a.Scoped {
		ok := a.Scope == "read" || a.Scope == "readwrite"
		for _, f := range tree {
			readable[f.ID] = ok
		}
		return readable, ok
	}

	// Build parent lookup.
	parent := make(map[int64]*int64, len(tree))
	for _, f := range tree {
		parent[f.ID] = f.ParentID
	}

	resolve := func(id int64) string {
		cur := &id
		visited := 0
		for cur != nil && visited < len(tree)+1 {
			if p, ok := a.byFolder[*cur]; ok {
				return p
			}
			cur = parent[*cur]
			visited++
		}
		// Walked all the way to root.
		return a.rootPerm
	}

	for _, f := range tree {
		p := resolve(f.ID)
		if p == "read" || p == "write" {
			readable[f.ID] = true
		}
	}
	return readable, a.rootPerm == "read" || a.rootPerm == "write"
}

// resolve walks the folder's ancestor chain and returns the nearest
// permission grant, or "" if no ancestor carries one. folderID=nil uses
// rootPerm directly.
func (a *TokenAuthz) resolve(ctx context.Context, folders repository.FolderRepository, userID int64, folderID *int64) (string, error) {
	if folderID == nil {
		return a.rootPerm, nil
	}
	// Walk up the chain, bounded to guard against cycles (folders are a DAG
	// and the DB enforces no cycles, but belt-and-suspenders).
	cur := *folderID
	for i := 0; i < 1024; i++ {
		if p, ok := a.byFolder[cur]; ok {
			return p, nil
		}
		f, err := folders.GetByID(ctx, userID, cur)
		if err != nil {
			// Folder not found or not owned by this user — no grant applies.
			if errors.Is(err, apperror.ErrNotFound) {
				return "", nil
			}
			return "", err
		}
		if f.ParentID == nil {
			// Reached a root-level folder with no direct grant; root grant
			// (if any) covers it.
			return a.rootPerm, nil
		}
		cur = *f.ParentID
	}
	return "", nil
}
