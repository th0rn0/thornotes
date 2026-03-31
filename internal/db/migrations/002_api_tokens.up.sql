CREATE TABLE IF NOT EXISTS api_tokens (
    id           INTEGER  PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT     NOT NULL DEFAULT 'Default',
    token        TEXT     NOT NULL UNIQUE,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    last_used_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_api_tokens_user_id ON api_tokens(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_tokens_token ON api_tokens(token);
