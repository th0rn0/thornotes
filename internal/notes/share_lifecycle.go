package notes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
)

// ShareViewWindow bounds how often a single share link may stamp last_viewed_at.
//
// The public share route is unauthenticated and read-heavy: anyone holding a URL
// can request it as fast as they like. A write per request would make every view
// a database write and hand the holder of a link a cheap way to grow the table,
// so the touch happens at most once per link per window.
//
// Ten minutes, because the question last_viewed_at answers is "is this link
// still being used, and when was it last handed out and read?" — not "how many
// times". Ten minutes bounds the write amplification to 6 writes per link per
// hour no matter the traffic, while a link that gets opened on Monday and again
// on Tuesday is still told apart from one nobody ever opened.
const ShareViewWindow = 10 * time.Minute

// Bounds on a share deadline, in years.
//
// The point is representability, not policy: a deadline reaches the driver as a
// time.Time, and a value the column cannot hold comes back as a driver error
// rather than an answer. MySQL's DATETIME stops at 1000-01-01; RFC3339 carries a
// four-digit year, so 9999 is the top of the JSON door, and 10000 or a year like
// 0500 would otherwise be accepted here and fail later, at the database, on a
// row somebody already owns.
const (
	shareExpiryMinYear = 1000
	shareExpiryMaxYear = 9999
)

// normalizeShareExpiry returns the deadline expressed in UTC, or a 400 when it
// falls outside what the column can hold. A nil deadline stays nil: no expiry is
// the default and must not be rewritten into anything.
//
// Why UTC and not the caller's offset: the SQLite driver stores a time.Time as
// its String() form — "2026-11-19 00:48:54 +0000 UTC" scans back into a
// time.Time, "2026-11-19 05:48:54 +0500 +0500" does not. A deadline carrying
// "+05:00" was therefore accepted with 200 and then poisoned the row: every read
// of that note AND the whole GET /api/v1/shares list failed with
// `sql: Scan error ... unsupported Scan, storing driver.Value type string into
// type *time.Time`, the link answered 404 for ever, and the startup reconcile
// stopped converging for that user. MariaDB round-trips the offset correctly, so
// this is a SQLite defect — but SQLite is the default driver, and normalising
// here makes both engines store the same instant regardless of what the client
// sent. That is the property worth having: the value the API accepted is the
// value that reads back, on either engine.
//
// The instant is preserved, only its representation changes: 05:48:54+05:00
// becomes 00:48:54Z, which is the same moment and the one the link expires at.
func normalizeShareExpiry(expiresAt *time.Time) (*time.Time, error) {
	if expiresAt == nil {
		return nil, nil
	}
	utc := expiresAt.UTC()
	if year := utc.Year(); year < shareExpiryMinYear || year > shareExpiryMaxYear {
		return nil, apperror.BadRequest(fmt.Sprintf("expires_at must be between %d-01-01 and %d-12-31",
			shareExpiryMinYear, shareExpiryMaxYear))
	}
	return &utc, nil
}

// GetNoteByShareToken returns the note behind a public share token, or
// apperror.ErrNotFound when there is no link or the link has expired.
//
// Expired and missing must be the same answer, and not merely the same status
// code: the handler serialises both outcomes through one branch, so a caller
// cannot tell a burned-out link from a typo. That is why the expiry check lives
// here and returns the same sentinel the repository returns for "no row" —
// a distinct error type would tempt a caller to distinguish them later.
func (s *Service) GetNoteByShareToken(ctx context.Context, token string) (*model.Note, error) {
	note, err := s.notes.GetByShareToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if ShareLinkExpired(note.ExpiresAt, time.Now().UTC()) {
		return nil, apperror.ErrNotFound
	}
	return note, nil
}

// ShareLinkExpired reports whether a link with this expiry has stopped
// answering at now. A nil expiry never expires: that is the default and the
// value every link created before ticket 601 carries, so it must stay live.
//
// The boundary is inclusive — a link whose deadline is exactly now is dead —
// because the alternative makes "expires at 12:00" answer at 12:00 exactly and
// leaves a one-second window a caller could observe.
func ShareLinkExpired(expiresAt *time.Time, now time.Time) bool {
	if expiresAt == nil {
		return false
	}
	return !now.Before(*expiresAt)
}

// SetShareToken generates a new share token for a note, or clears the existing
// one (clear=true), and applies the requested expiry.
//
// expiresAt is opt-in: nil keeps the link immortal, which is the documented
// default and what every existing link already has. Clearing a share clears its
// expiry too — a note that is not shared must not carry a deadline that would
// silently apply to the next link somebody creates.
func (s *Service) SetShareToken(ctx context.Context, userID, noteID int64, clear bool, expiresAt *time.Time) (*string, error) {
	if clear {
		// The deadline goes first: SetShareExpiry only writes to a row that still
		// has a token, so clearing the token first would leave expires_at behind
		// and a note that is not shared would silently carry a deadline into its
		// next link.
		//
		// Not fatal when it fails: a note that was never shared has no deadline
		// to drop, and the token clear below reports not-found for the caller.
		if err := s.notes.SetShareExpiry(ctx, userID, noteID, nil); err != nil && !errors.Is(err, apperror.ErrNotFound) {
			log.Warn().Err(err).Int64("note_id", noteID).Msg("clear share expiry before revoking token")
		}
		return nil, s.notes.SetShareToken(ctx, userID, noteID, nil)
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, apperror.Internal("generate share token", err)
	}
	token := hex.EncodeToString(b)

	// Normalised before either write: a deadline the column cannot hold is
	// refused while the row is still untouched, rather than after a token has
	// been issued for a link that can never be read. The clear path above is
	// deliberately not subject to this — revoking a link must not depend on a
	// field it ignores.
	expiry, err := normalizeShareExpiry(expiresAt)
	if err != nil {
		return nil, err
	}

	if err := s.notes.SetShareToken(ctx, userID, noteID, &token); err != nil {
		return nil, fmt.Errorf("set share token: %w", err)
	}
	// Always written, including the nil case: the expiry belongs to the link
	// that was just created, so leaving a previous deadline on the row would
	// make a re-shared link without an expiry silently die at the old one's
	// deadline.
	if err := s.notes.SetShareExpiry(ctx, userID, noteID, expiry); err != nil {
		// The token landed but the deadline did not. Reporting success would
		// claim an expiry the row does not have, so this is an error — the
		// caller can clear the share and retry.
		return nil, fmt.Errorf("set share expiry: %w", err)
	}
	return &token, nil
}

// SetShareExpiry changes the deadline on an existing link (nil = no expiry)
// without rotating the token, so URLs already handed out keep working.
//
// The deadline is normalised to UTC here for the same reason SetShareToken does
// it: PATCH is the other door into expires_at, and leaving one of them
// un-normalised would leave the SQLite poisoning reachable.
func (s *Service) SetShareExpiry(ctx context.Context, userID, noteID int64, expiresAt *time.Time) error {
	expiry, err := normalizeShareExpiry(expiresAt)
	if err != nil {
		return err
	}
	return s.notes.SetShareExpiry(ctx, userID, noteID, expiry)
}

// ListShares returns the caller's own share links, token redacted to a prefix.
// It is scoped by user_id in SQL, so it can only ever answer with the caller's
// own links.
func (s *Service) ListShares(ctx context.Context, userID int64) ([]model.ShareLink, error) {
	return s.notes.ListShares(ctx, userID)
}

// RecordShareView records a view of a share link at now, subject to
// ShareViewWindow.
//
// lastViewedAt is the value the caller just read for that link; passing it back
// is what lets the bound be decided without a second read, and it is
// deliberately not derived here — a fresh read would make every request a read
// plus a possible write on an unauthenticated route.
//
// It reports whether a row was written. Errors are the caller's to log and
// discard: a view that cannot be counted must never turn into a failed response
// for someone reading a note.
func (s *Service) RecordShareView(ctx context.Context, noteID int64, lastViewedAt *time.Time, now time.Time) (bool, error) {
	return s.notes.TouchShareView(ctx, noteID, lastViewedAt, now, ShareViewWindow)
}

// ShareViewDue reports whether a view at now is worth writing, given the last
// view already recorded. It mirrors repository.ShareViewDue against the
// service's own window, so a caller can skip the async write entirely instead of
// scheduling one that the bound would reject.
func ShareViewDue(lastViewedAt *time.Time, now time.Time) bool {
	return repository.ShareViewDue(lastViewedAt, now, ShareViewWindow)
}
