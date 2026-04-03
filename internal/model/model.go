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
	ID          int64      `json:"id"`
	UserID      int64      `json:"-"`
	FolderID    *int64     `json:"folder_id"`
	Title       string     `json:"title"`
	Slug        string     `json:"slug"`
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

// NoteWatchRecord is a lightweight projection used by the disk watcher.
// It contains only the fields needed to detect content changes.
type NoteWatchRecord struct {
	ID          int64
	DiskPath    string
	ContentHash string
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
	ID          int64      `json:"id"`
	UserID      int64      `json:"-"`
	Name        string     `json:"name"`
	Token       string     `json:"token,omitempty"` // only set on creation
	Prefix      string     `json:"prefix"`          // first 8 chars for display
	Scope       string     `json:"scope"`           // "readwrite" or "read"
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
}
