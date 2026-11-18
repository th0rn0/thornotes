-- Down for 012_folder_shares. Structurally tested, never run automatically:
-- the app refuses a schema newer than the build (internal/db/schema.go) rather
-- than downgrading itself, because dropping this table discards every folder
-- link that was handed out, with nothing to restore it from.
DROP TABLE IF EXISTS folder_shares;
