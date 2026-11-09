package model

import "time"

type User struct {
	ID           int64     `json:"id"`
	UUID         string    `json:"uuid"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	// TokenScope is set by BearerMiddleware when a request is authenticated via
	// an API token. Empty string means session auth (full access).
	TokenScope string `json:"-"`
}

type Session struct {
	Token     string    `json:"token"`
	UserID    int64     `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

type Folder struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"-"`
	ParentID  *int64    `json:"parent_id"`
	Name      string    `json:"name"`
	DiskPath  string    `json:"disk_path"`
	CreatedAt time.Time `json:"created_at"`
}

type Note struct {
	ID       int64  `json:"id"`
	UserID   int64  `json:"-"`
	FolderID *int64 `json:"folder_id"`
	Title    string `json:"title"`
	Slug     string `json:"slug"`
	// TitleKey is the normalized lookup key for Title (NFC, entity-decoded,
	// dash-folded, case-folded). Computed in Go; used for link resolution.
	TitleKey    string     `json:"-"`
	DiskPath    string     `json:"disk_path"`
	Content     string     `json:"content"`
	ContentHash string     `json:"content_hash"`
	Tags        []string   `json:"tags"`
	ShareToken  *string    `json:"share_token,omitempty"`
	FtsSyncedAt *time.Time `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type NoteListItem struct {
	ID        int64     `json:"id"`
	FolderID  *int64    `json:"folder_id"`
	Title     string    `json:"title"`
	Slug      string    `json:"slug"`
	Tags      []string  `json:"tags"`
	UpdatedAt time.Time `json:"updated_at"`
}

type FolderTreeItem struct {
	ID         int64  `json:"id"`
	ParentID   *int64 `json:"parent_id"`
	Name       string `json:"name"`
	DiskPath   string `json:"disk_path"`
	ChildCount int    `json:"child_count"`
	NoteCount  int    `json:"note_count"`
}

type SearchResult struct {
	NoteID  int64    `json:"note_id"`
	Title   string   `json:"title"`
	Slug    string   `json:"slug"`
	Snippet string   `json:"snippet"`
	Tags    []string `json:"tags"`
}

// NoteWatchRecord is a lightweight projection used by the disk watcher and
// the filesystem reconciliation walk. Slug and FolderID are needed by the
// reconciler to detect a stale disk_path (e.g. a parent folder was renamed
// but the descendant cascade DB update failed) and repair it instead of
// destroying the row.
type NoteWatchRecord struct {
	ID          int64
	FolderID    *int64
	Slug        string
	DiskPath    string
	ContentHash string
}

// NoteExportRecord is the metadata a zip export needs from the database. The
// archive name comes from Title, the bytes are streamed from DiskPath, and the
// entry's modified time comes from UpdatedAt. Content is deliberately absent:
// the exporter opens each file as it writes the entry, so an export can never
// ship bytes the disk does not have.
type NoteExportRecord struct {
	ID        int64
	FolderID  *int64
	Title     string
	DiskPath  string
	UpdatedAt time.Time
}

type Journal struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// HistoryEntry represents a single commit in a note's git history.
type HistoryEntry struct {
	SHA       string    `json:"sha"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// HistoryEntryContent is returned by the "note at commit" endpoint.
type HistoryEntryContent struct {
	SHA       string    `json:"sha"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type APIToken struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"-"`
	Name       string     `json:"name"`
	Token      string     `json:"token,omitempty"` // only set on creation
	Prefix     string     `json:"prefix"`          // first 8 chars for display
	Scope      string     `json:"scope"`           // "readwrite" or "read" — global fallback when no folder permissions exist
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	// FolderPermissions, when non-empty, constrains the token to the listed
	// folders (whitelist mode). When empty, the token uses Scope globally.
	// A nil FolderID represents a permission on the root (unfiled) area.
	FolderPermissions []TokenFolderPermission `json:"folder_permissions,omitempty"`
}

// NoteLink is one row in note_links — a resolved outgoing [[link]] edge.
type NoteLink struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"-"`
	FromNoteID int64      `json:"from_note_id"`
	ToNoteID   *int64     `json:"to_note_id"` // nil = unresolved/broken
	RawTarget  string     `json:"raw_target"`
	TargetHash string     `json:"target_hash"` // sha256(NormalizeKey(target))
	Anchor     string     `json:"anchor"`
	LinkText   string     `json:"link_text"`
	Ambiguous  bool       `json:"ambiguous"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Backlink is an inbound edge, enriched with the source note's title and
// folder for display ("what links here").
type Backlink struct {
	FromNoteID   int64  `json:"from_note_id"`
	FromTitle    string `json:"from_title"`
	FromFolderID *int64 `json:"from_folder_id"`
	Anchor       string `json:"anchor"`
	LinkText     string `json:"link_text"`
}

// InboundLink is an inbound edge targeted for re-resolution after a rename.
type InboundLink struct {
	ID           int64
	FromNoteID   int64
	FromFolderID *int64
	RawTarget    string
}

// TokenFolderPermission grants a single API token permission on one folder.
// FolderID is nil when the permission covers the root (unfiled) area.
// Permission is "read" or "write"; "write" implies read. Permission on a
// folder cascades to its descendants at enforcement time.
type TokenFolderPermission struct {
	FolderID   *int64 `json:"folder_id"`
	Permission string `json:"permission"`
}
