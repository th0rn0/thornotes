CREATE TABLE journals (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL COLLATE NOCASE,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(user_id, name)
);
