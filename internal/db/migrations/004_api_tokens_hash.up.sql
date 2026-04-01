-- Add prefix column (first 8 chars of raw token, for display).
ALTER TABLE api_tokens ADD COLUMN prefix TEXT NOT NULL DEFAULT '';
UPDATE api_tokens SET prefix = substr(token, 1, 8);

-- Rename token → token_hash.
-- Existing stored values are plaintext; they will no longer match hashed lookups,
-- which effectively invalidates all pre-migration tokens. Users must create new tokens.
ALTER TABLE api_tokens RENAME COLUMN token TO token_hash;
