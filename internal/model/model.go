package model

import "time"

type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
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
