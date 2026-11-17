package notes_test

import (
	"context"
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
