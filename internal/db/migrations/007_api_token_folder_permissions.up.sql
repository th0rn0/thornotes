-- Per-folder permissions for an API token. When a token has zero rows in
-- this table it falls back to the global `scope` on api_tokens (the existing
-- read/readwrite behavior). When it has any rows, the token is in "scoped
-- mode" — access is granted only to folders listed here (and their
-- descendants, via parent-chain walk at enforcement time). A row with a
-- NULL folder_id represents a permission on the root (unfiled) area.
--
-- permission is either 'read' or 'write'; 'write' implies read.
CREATE TABLE IF NOT EXISTS api_token_folder_permissions (
    id          INTEGER  PRIMARY KEY AUTOINCREMENT,
    token_id    INTEGER  NOT NULL REFERENCES api_tokens(id) ON DELETE CASCADE,
    folder_id   INTEGER  REFERENCES folders(id) ON DELETE CASCADE,
    permission  TEXT     NOT NULL CHECK(permission IN ('read', 'write')),
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- One permission row per (token, folder). Two partial indexes handle NULL
-- folder_id (root) the same way folders_root_unique does for folders.
CREATE UNIQUE INDEX IF NOT EXISTS api_token_folder_permissions_folder_unique
    ON api_token_folder_permissions(token_id, folder_id)
    WHERE folder_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS api_token_folder_permissions_root_unique
    ON api_token_folder_permissions(token_id)
    WHERE folder_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_api_token_folder_permissions_token
    ON api_token_folder_permissions(token_id);
