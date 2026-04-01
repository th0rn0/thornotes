ALTER TABLE api_tokens RENAME COLUMN token_hash TO token;
ALTER TABLE api_tokens DROP COLUMN prefix;
