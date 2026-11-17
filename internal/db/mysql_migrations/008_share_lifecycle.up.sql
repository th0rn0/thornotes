-- Share-link lifecycle (ticket 601, item 3): an opt-in expiry and a bounded
-- "last viewed" stamp on the share_token capability that already exists.
--
-- Both columns are nullable with an explicit NULL default, and that is the
-- whole point: NULL expires_at means "no expiry", which stays the default. The
-- migration is additive — no table rebuild, no row rewritten, no existing
-- share_token touched — so an upgrade cannot retro-expire a working link.
--
-- expires_at     when the link stops answering (NULL = never)
-- last_viewed_at the last view recorded for the link, at most one write per
--                link per ShareViewWindow (internal/notes/share_lifecycle.go)
ALTER TABLE notes
    ADD COLUMN expires_at     DATETIME NULL DEFAULT NULL,
    ADD COLUMN last_viewed_at DATETIME NULL DEFAULT NULL;
