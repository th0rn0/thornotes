package notes_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/notes"
)

// The service-level rules of the share-link lifecycle: an expired link is
// not-found and nothing else, the expiry belongs to the link that is current,
// and the view stamp is bounded.

func TestShareLinkExpired_Boundary(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

	assert.False(t, notes.ShareLinkExpired(nil, now), "no expiry never expires: that is the default")

	justAfter := now.Add(-time.Nanosecond)
	assert.True(t, notes.ShareLinkExpired(&justAfter, now))

	// The deadline is inclusive. "Expires at 12:00" must not answer at 12:00 —
	// a one-second "still live" window is observable and unexplainable.
	exactly := now
	assert.True(t, notes.ShareLinkExpired(&exactly, now))

	later := now.Add(time.Nanosecond)
	assert.False(t, notes.ShareLinkExpired(&later, now))
}

// TestShareViewWindow_IsMinutesNotSeconds pins the bound the ticket asks to be
// stated: ten minutes, which caps a link at six recorded views an hour no matter
// how hard it is hit.
func TestShareViewWindow_IsMinutesNotSeconds(t *testing.T) {
	assert.Equal(t, 10*time.Minute, notes.ShareViewWindow)
}

func TestService_ExpiredShareIsNotFound(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Expiring", nil)
	require.NoError(t, err)

	// A deadline in the past. The HTTP layer refuses to create one of these; the
	// service must still refuse to serve the link, because time passing produces
	// exactly this state without anyone calling the API.
	past := time.Now().UTC().Add(-time.Minute)
	token, err := st.svc.SetShareToken(ctx, st.userID, note.ID, false, &past)
	require.NoError(t, err)
	require.NotNil(t, token)

	_, err = st.svc.GetNoteByShareToken(ctx, *token)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound,
		"an expired link must be the same not-found a wrong token produces, not a distinct 'expired' error")

	// Nothing about the row was destroyed: the owner can still see it, and a new
	// deadline revives the same URL.
	_, err = st.svc.GetNote(ctx, st.userID, note.ID)
	require.NoError(t, err)

	future := time.Now().UTC().Add(time.Hour)
	require.NoError(t, st.svc.SetShareExpiry(ctx, st.userID, note.ID, &future))
	revived, err := st.svc.GetNoteByShareToken(ctx, *token)
	require.NoError(t, err)
	assert.Equal(t, note.ID, revived.ID)
}

// TestService_ClearShareClearsExpiry keeps the invariant "expires_at is set
// exactly when a link exists": a note that is no longer shared must not carry a
// deadline, or the next link silently inherits it.
func TestService_ClearShareClearsExpiry(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Revoked", nil)
	require.NoError(t, err)

	deadline := time.Now().UTC().Add(time.Hour)
	token, err := st.svc.SetShareToken(ctx, st.userID, note.ID, false, &deadline)
	require.NoError(t, err)
	require.NotNil(t, token)

	cleared, err := st.svc.SetShareToken(ctx, st.userID, note.ID, true, nil)
	require.NoError(t, err)
	assert.Nil(t, cleared)

	var expiresAt *time.Time
	require.NoError(t, st.pool.ReadDB.QueryRow(
		`SELECT expires_at FROM notes WHERE id = ?`, note.ID).Scan(&expiresAt))
	assert.Nil(t, expiresAt, "clearing the share must clear the deadline with it")

	// The revoked link is dead, which is unchanged behaviour.
	_, err = st.svc.GetNoteByShareToken(ctx, *token)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

// TestService_ReshareResetsExpiry is the stale-deadline trap: a link that was
// given a deadline, then cleared, then re-shared WITHOUT one must not die at the
// old deadline.
func TestService_ReshareResetsExpiry(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Reshared", nil)
	require.NoError(t, err)

	deadline := time.Now().UTC().Add(time.Hour)
	_, err = st.svc.SetShareToken(ctx, st.userID, note.ID, false, &deadline)
	require.NoError(t, err)
	_, err = st.svc.SetShareToken(ctx, st.userID, note.ID, true, nil)
	require.NoError(t, err)

	// Re-share with no expiry. The new link must be immortal.
	token, err := st.svc.SetShareToken(ctx, st.userID, note.ID, false, nil)
	require.NoError(t, err)
	require.NotNil(t, token)

	var expiresAt *time.Time
	require.NoError(t, st.pool.ReadDB.QueryRow(
		`SELECT expires_at FROM notes WHERE id = ?`, note.ID).Scan(&expiresAt))
	assert.Nil(t, expiresAt, "a re-share without an expiry must not inherit the previous deadline")

	_, err = st.svc.GetNoteByShareToken(ctx, *token)
	require.NoError(t, err)
}

// TestService_RecordShareView_IsBoundedByTheWindow drives the bound through the
// service, which is the layer that owns the window: inside it nothing is
// written, outside it the next view is.
func TestService_RecordShareView_IsBoundedByTheWindow(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Viewed", nil)
	require.NoError(t, err)
	token, err := st.svc.SetShareToken(ctx, st.userID, note.ID, false, nil)
	require.NoError(t, err)
	require.NotNil(t, token)

	start := time.Now().UTC()

	first, err := st.svc.RecordShareView(ctx, note.ID, nil, start)
	require.NoError(t, err)
	require.True(t, first, "the first view of a link must be recorded")

	insideWindow, err := st.svc.RecordShareView(ctx, note.ID, &start, start.Add(notes.ShareViewWindow-time.Second))
	require.NoError(t, err)
	assert.False(t, insideWindow, "a view inside the window must not write")

	outsideWindow, err := st.svc.RecordShareView(ctx, note.ID, &start, start.Add(notes.ShareViewWindow))
	require.NoError(t, err)
	assert.True(t, outsideWindow, "the first view outside the window must write")

	// And the pure predicate agrees with the repository: never-viewed is due.
	assert.True(t, notes.ShareViewDue(nil, start))
	assert.False(t, notes.ShareViewDue(&start, start.Add(time.Minute)))
}

// storedDeadline reads the deadline as the database holds it. The API's own
// response echoes what was sent, so only the stored value can prove the
// representation is one that reads back.
func storedDeadline(t *testing.T, st *serviceStack, noteID int64) *time.Time {
	t.Helper()
	var expiresAt *time.Time
	require.NoError(t, st.pool.ReadDB.QueryRow(
		`SELECT expires_at FROM notes WHERE id = ?`, noteID).Scan(&expiresAt))
	return expiresAt
}

// TestService_ShareExpiryIsStoredInUTC covers both doors into expires_at at the
// layer that owns the normalisation. The SQLite driver stores a time.Time in its
// String() form, and only the UTC spelling scans back into one: "+05:00" became
// "… +0500 +0500", which made every read of the note and of the share list fail.
// The offset is a spelling of an instant, so the fix preserves the instant and
// changes the spelling — asserted on both.
func TestService_ShareExpiryIsStoredInUTC(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Offset deadline", nil)
	require.NoError(t, err)

	east := time.FixedZone("+05:00", 5*60*60)
	west := time.FixedZone("-08:00", -8*60*60)

	posted := time.Date(2026, 11, 19, 5, 48, 54, 0, east)
	token, err := st.svc.SetShareToken(ctx, st.userID, note.ID, false, &posted)
	require.NoError(t, err)
	require.NotNil(t, token)

	stored := storedDeadline(t, st, note.ID)
	require.NotNil(t, stored, "the deadline must have been written")
	assert.True(t, stored.Equal(posted),
		"the stored instant must be the instant that was sent: stored %s, sent %s", stored, posted)
	_, offset := stored.Zone()
	assert.Zero(t, offset, "the stored deadline must be UTC, not the caller's offset: %s", stored)

	// The link still resolves. When this was wrong, the scan failed and every
	// read of the note answered 500 — the link was dead, not expired.
	resolved, err := st.svc.GetNoteByShareToken(ctx, *token)
	require.NoError(t, err)
	assert.Equal(t, note.ID, resolved.ID)

	// Door two: PATCH, on the same link, with the other direction.
	patched := time.Date(2026, 11, 18, 16, 48, 54, 0, west)
	require.NoError(t, st.svc.SetShareExpiry(ctx, st.userID, note.ID, &patched))

	stored = storedDeadline(t, st, note.ID)
	require.NotNil(t, stored)
	assert.True(t, stored.Equal(patched),
		"a PATCHed deadline must store the instant that was sent: stored %s, sent %s", stored, patched)
	_, offset = stored.Zone()
	assert.Zero(t, offset, "a PATCHed deadline must be UTC: %s", stored)

	// The list scans every share row, so a poisoned one took all of them with it.
	links, err := st.svc.ListShares(ctx, st.userID)
	require.NoError(t, err)
	require.Len(t, links, 1)
	require.NotNil(t, links[0].ExpiresAt)
	assert.True(t, links[0].ExpiresAt.Equal(patched),
		"the list must report the stored instant, not a shifted one")
}

// TestService_ShareExpiryOutOfRangeIsRefused: a year the column cannot hold must
// be answered here, as a 400 on an untouched row, rather than arriving as a
// driver error on a row somebody already owns. The deadline does not exist in
// production yet, so this is the cheap moment to be strict about it.
func TestService_ShareExpiryOutOfRangeIsRefused(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Out of range", nil)
	require.NoError(t, err)

	outOfRange := []struct {
		name string
		at   time.Time
	}{
		{"above the column", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"below the column", time.Date(500, 1, 1, 0, 0, 0, 0, time.UTC)},
	}

	for _, tc := range outOfRange {
		t.Run(tc.name, func(t *testing.T) {
			at := tc.at
			_, err := st.svc.SetShareToken(ctx, st.userID, note.ID, false, &at)
			require.Error(t, err)

			var appErr *apperror.AppError
			require.ErrorAs(t, err, &appErr)
			assert.Equal(t, http.StatusBadRequest, appErr.Code)

			// Refused before any write: no token may be issued for a link whose
			// deadline the database cannot hold.
			var token *string
			require.NoError(t, st.pool.ReadDB.QueryRow(
				`SELECT share_token FROM notes WHERE id = ?`, note.ID).Scan(&token))
			assert.Nil(t, token, "a refused deadline must not leave a token behind")
		})
	}

	// And the PATCH door refuses it on a live link, leaving that link working.
	token, err := st.svc.SetShareToken(ctx, st.userID, note.ID, false, nil)
	require.NoError(t, err)
	require.NotNil(t, token)

	tooFar := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	err = st.svc.SetShareExpiry(ctx, st.userID, note.ID, &tooFar)
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, http.StatusBadRequest, appErr.Code)

	assert.Nil(t, storedDeadline(t, st, note.ID), "a refused PATCH must not write a deadline")
	_, err = st.svc.GetNoteByShareToken(ctx, *token)
	require.NoError(t, err, "a refused PATCH must leave the link alone")
}

// TestService_ReShareResetsLastViewedAt pins the rotation rule at the service
// layer: the view stamp is part of the link, not of the note.
func TestService_ReShareResetsLastViewedAt(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Reshared views", nil)
	require.NoError(t, err)

	first, err := st.svc.SetShareToken(ctx, st.userID, note.ID, false, nil)
	require.NoError(t, err)
	require.NotNil(t, first)

	start := time.Now().UTC()
	wrote, err := st.svc.RecordShareView(ctx, note.ID, nil, start)
	require.NoError(t, err)
	require.True(t, wrote)

	var viewed *time.Time
	require.NoError(t, st.pool.ReadDB.QueryRow(
		`SELECT last_viewed_at FROM notes WHERE id = ?`, note.ID).Scan(&viewed))
	require.NotNil(t, viewed, "the view must have been recorded")

	// Re-share: a new token, and with it a link with no history.
	second, err := st.svc.SetShareToken(ctx, st.userID, note.ID, false, nil)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.NotEqual(t, *first, *second)

	var inherited *time.Time
	require.NoError(t, st.pool.ReadDB.QueryRow(
		`SELECT last_viewed_at FROM notes WHERE id = ?`, note.ID).Scan(&inherited))
	assert.Nil(t, inherited, "a rotated token must not inherit the previous link's view time")

	// The new link's first view is due, rather than suppressed by the old stamp.
	assert.True(t, notes.ShareViewDue(inherited, start.Add(time.Minute)),
		"a link that has never been viewed must be due a stamp")
}
