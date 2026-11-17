-- Down for 011_share_lifecycle. Structurally tested, never run automatically:
-- the app refuses a schema newer than the build (internal/db/schema.go) rather
-- than downgrading itself, because dropping these columns would discard the
-- expiry of every shared link with no way to tell it apart from "no expiry".
ALTER TABLE notes DROP COLUMN last_viewed_at;
ALTER TABLE notes DROP COLUMN expires_at;
