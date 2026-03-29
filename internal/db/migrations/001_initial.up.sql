CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT    NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT     NOT NULL PRIMARY KEY,
    user_id    INTEGER  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS folders (
    id         INTEGER  PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    parent_id  INTEGER  REFERENCES folders(id) ON DELETE CASCADE,
    name       TEXT     NOT NULL,
    disk_path  TEXT     NOT NULL UNIQUE,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- Two partial indexes to handle NULL != NULL in SQLite UNIQUE constraints.
-- Without this, multiple root folders with the same name are allowed.
CREATE UNIQUE INDEX IF NOT EXISTS folders_root_unique
    ON folders(user_id, name) WHERE parent_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS folders_child_unique
    ON folders(user_id, parent_id, name) WHERE parent_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_folders_user_id ON folders(user_id);
CREATE INDEX IF NOT EXISTS idx_folders_parent_id ON folders(parent_id);

CREATE TABLE IF NOT EXISTS notes (
    id            INTEGER  PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    folder_id     INTEGER  REFERENCES folders(id) ON DELETE SET NULL,
    title         TEXT     NOT NULL,
    slug          TEXT     NOT NULL,
    disk_path     TEXT     NOT NULL UNIQUE,
    content       TEXT     NOT NULL DEFAULT '',
    content_hash  TEXT     NOT NULL DEFAULT '',
    tags          TEXT     NOT NULL DEFAULT '[]',
    share_token   TEXT     UNIQUE,
    fts_synced_at DATETIME,
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at    DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_notes_slug_folder
    ON notes(user_id, folder_id, slug) WHERE folder_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_notes_slug_root
    ON notes(user_id, slug) WHERE folder_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_notes_share_token
    ON notes(share_token) WHERE share_token IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_notes_user_id ON notes(user_id);
CREATE INDEX IF NOT EXISTS idx_notes_folder_id ON notes(folder_id);

-- FTS5 virtual table. External content mode — no triggers.
-- Sync is deferred to search time via fts_synced_at column.
CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
    title,
    content,
    content=notes,
    content_rowid=id,
    tokenize='porter ascii'
);
