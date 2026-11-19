package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
)

// Share-link lifecycle at the repository level: the counted touch bound and the
// redacted enumeration. Both are asserted here against the real SQLite
// statements rather than against a fake, because what is being pinned is the
// SQL predicate that implements the bound.

// linkNote inserts a note with its own slug and (optionally) a share token.
func linkNote(t *testing.T, repo *NoteRepo, userID int64, title, slug string, token *string) *model.Note {
	t.Helper()
	n := &model.Note{
		UserID:      userID,
		Title:       title,
		Slug:        slug,
		DiskPath:    "1/" + slug + ".md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	}
	created, err := repo.Create(context.Background(), n)
	require.NoError(t, err)
	if token != nil {
		require.NoError(t, repo.SetShareToken(context.Background(), userID, created.ID, token))
		created.ShareToken = token
	}
	return created
}

// storedView reads the row's current last_viewed_at, which is what "a write
// happened" means on disk.
func storedView(t *testing.T, pool *db.Pool, noteID int64) sql.NullTime {
	t.Helper()
	var v sql.NullTime
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT last_viewed_at FROM notes WHERE id = ?`, noteID).Scan(&v))
	return v
}

// TestNoteRepo_TouchShareView_CountedBound is the acceptance test for the touch
// bound: the writes are counted, not asserted in prose.
//
// Five requests inside the window must produce exactly one last_viewed_at write,
// and the first request outside the window must produce the second.
func TestNoteRepo_TouchShareView_CountedBound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	token := "counted0000000000000000000000001"
	note := linkNote(t, repo, user.ID, "Counted Views", "counted-views", &token)

	const window = 10 * time.Minute
	start := time.Now().UTC()

	// N requests inside the window. Each one re-reads the row first, exactly as
	// the handler does — the bound must hold for that path, not only for a
	// caller that happens to keep the value.
	const requests = 5
	writes := 0
	for i := 0; i < requests; i++ {
		at := start.Add(time.Duration(i) * time.Second)

		current, err := repo.GetByShareToken(ctx, token)
		require.NoError(t, err)

		wrote, err := repo.TouchShareView(ctx, note.ID, current.LastViewedAt, at, window)
		require.NoError(t, err)
		if wrote {
			writes++
		}
	}

	assert.Equal(t, 1, writes,
		"%d requests inside the %s window must produce exactly one last_viewed_at write", requests, window)

	// The single write is the FIRST request's timestamp: had a later request
	// also written, the stored value would have moved.
	stored := storedView(t, pool, note.ID)
	require.True(t, stored.Valid, "the first view must have been recorded")
	assert.Equal(t, start.Truncate(time.Second), stored.Time.UTC().Truncate(time.Second),
		"the stored value must be the first view in the window, so the rest wrote nothing")

	// The next request outside the window produces the second write.
	outside := start.Add(window + time.Minute)
	current, err := repo.GetByShareToken(ctx, token)
	require.NoError(t, err)
	wrote, err := repo.TouchShareView(ctx, note.ID, current.LastViewedAt, outside, window)
	require.NoError(t, err)
	require.True(t, wrote, "the first request outside the window must write")
	writes++

	assert.Equal(t, 2, writes, "one write per window, not one per request")

	stored = storedView(t, pool, note.ID)
	require.True(t, stored.Valid)
	assert.Equal(t, outside.Truncate(time.Second), stored.Time.UTC().Truncate(time.Second),
		"the second write must carry the first timestamp outside the window")
}

// TestNoteRepo_TouchShareView_SQLGuardHoldsWithoutTheGoCheck pins the second
// layer: even when the caller passes a nil previous (the value the handler would
// have read before a concurrent request wrote), the statement itself refuses to
// stamp the same link twice inside the window.
//
// This is the race the bound would otherwise lose: two views read the same row
// at once, both see "never viewed", and both would write if the Go-side
// comparison were the only guard.
func TestNoteRepo_TouchShareView_SQLGuardHoldsWithoutTheGoCheck(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	token := "raceguard00000000000000000000001"
	note := linkNote(t, repo, user.ID, "Race Guard", "race-guard", &token)

	now := time.Now().UTC()
	wrote, err := repo.TouchShareView(ctx, note.ID, nil, now, 10*time.Minute)
	require.NoError(t, err)
	require.True(t, wrote)

	// Same window, both callers claiming they saw nothing.
	wrote, err = repo.TouchShareView(ctx, note.ID, nil, now.Add(time.Second), 10*time.Minute)
	require.NoError(t, err)
	assert.False(t, wrote, "the SQL predicate must refuse a second stamp inside the window")
}

// TestNoteRepo_TouchShareView_UnsharedNote proves the touch is tied to a live
// link: a note that is not shared cannot grow a last_viewed_at.
func TestNoteRepo_TouchShareView_UnsharedNote(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	note := linkNote(t, repo, user.ID, "Not Shared", "not-shared", nil)

	wrote, err := repo.TouchShareView(ctx, note.ID, nil, time.Now().UTC(), 10*time.Minute)
	require.NoError(t, err)
	assert.False(t, wrote)
	assert.False(t, storedView(t, pool, note.ID).Valid)
}

// TestNoteRepo_ListShares_RedactsToken is the acceptance test for the
// enumeration endpoint's payload: the caller's own links, with the token cut to
// a prefix and no way to reconstruct the rest.
func TestNoteRepo_ListShares_RedactsToken(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	other := NewUserRepo(pool.WriteDB)
	otherUser, err := other.Create(context.Background(), "other", "hash", "uuid-other")
	require.NoError(t, err)

	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	tokenA := "0123456789abcdef0123456789abcdef"
	tokenB := "fedcba9876543210fedcba9876543210"
	foreign := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	alpha := linkNote(t, repo, user.ID, "Alpha", "alpha", &tokenA)
	beta := linkNote(t, repo, user.ID, "Beta", "beta", &tokenB)
	linkNote(t, repo, user.ID, "Private", "private", nil)
	linkNote(t, repo, otherUser.ID, "Foreign", "foreign", &foreign)

	// Give alpha a deadline and a view, so both columns are exercised.
	deadline := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	require.NoError(t, repo.SetShareExpiry(ctx, user.ID, alpha.ID, &deadline))
	touched, err := repo.TouchShareView(ctx, alpha.ID, nil, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, touched)

	shares, err := repo.ListShares(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, shares, 2, "only the caller's own shared notes, and only the shared ones")

	byNote := map[int64]model.ShareLink{}
	for _, s := range shares {
		require.NotNil(t, s.NoteID, "a note row carries its note id")
		assert.Equal(t, model.ShareKindNote, s.Kind)
		byNote[*s.NoteID] = s
		// Whatever the row carries, it must never carry the token.
		assert.NotEqual(t, tokenA, s.TokenPrefix)
		assert.NotEqual(t, tokenB, s.TokenPrefix)
		assert.NotContains(t, s.TokenPrefix, tokenA[8:], "the body of the token must not leak")
	}

	a, ok := byNote[alpha.ID]
	require.True(t, ok)
	assert.Equal(t, "Alpha", a.Title)
	assert.Equal(t, tokenA[:8]+"…", a.TokenPrefix, "the prefix plus an ellipsis, so it cannot read as a whole token")
	require.NotNil(t, a.ExpiresAt, "the deadline must be visible in the list: it is what a bulk revoke needs to see")
	require.NotNil(t, a.LastViewedAt)

	b, ok := byNote[beta.ID]
	require.True(t, ok)
	assert.Equal(t, "Beta", b.Title)
	assert.Equal(t, tokenB[:8]+"…", b.TokenPrefix)
	assert.Nil(t, b.ExpiresAt, "no expiry is the default and must read as null, not as a zero time")
	assert.Nil(t, b.LastViewedAt)

	// A different user's list must not contain any of these.
	otherShares, err := repo.ListShares(ctx, otherUser.ID)
	require.NoError(t, err)
	require.Len(t, otherShares, 1)
	assert.Equal(t, "Foreign", otherShares[0].Title)
	for _, s := range otherShares {
		require.NotNil(t, s.NoteID)
		assert.NotEqual(t, alpha.ID, *s.NoteID)
		assert.NotEqual(t, beta.ID, *s.NoteID)
	}
}

// TestNoteRepo_ListShares_EmptyIsNotAnError keeps the "no shares" answer a shape
// the handler can serialise as [] rather than null.
func TestNoteRepo_ListShares_Empty(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	linkNote(t, repo, user.ID, "Unshared", "unshared", nil)

	shares, err := repo.ListShares(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Empty(t, shares)
}

// TestNoteRepo_SetShareExpiry_UnsharedNoteIsNotFound: a deadline on a note that
// has no link is a value nothing reads, so it must not be reported as success.
func TestNoteRepo_SetShareExpiry_UnsharedNoteIsNotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	note := linkNote(t, repo, user.ID, "No Link", "no-link", nil)

	deadline := time.Now().Add(time.Hour)
	err := repo.SetShareExpiry(context.Background(), user.ID, note.ID, &deadline)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

// storedExpiry reads the deadline as the row holds it, which is the only way to
// prove a refused write wrote nothing: the caller who is refused gets an error,
// not a value to compare against.
func storedExpiry(t *testing.T, pool *db.Pool, noteID int64) *time.Time {
	t.Helper()
	var expiresAt *time.Time
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT expires_at FROM notes WHERE id = ?`, noteID).Scan(&expiresAt))
	return expiresAt
}

// TestNoteRepo_SetShareExpiry_ExpiredLinkIsRefused is the SQLite leg of "expired
// is final", and it pins BOTH write shapes: a new future deadline, and a clear.
//
// Clearing is the one that is easy to miss. expires_at = NULL does not extend a
// dead link, it makes it immortal — a stronger revival than the future-dated
// write — so a guard that only tests the shape a caller reaches for first would
// leave the more thorough revival open.
func TestNoteRepo_SetShareExpiry_ExpiredLinkIsRefused(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	token := "expired0000000000000000000000001"
	note := linkNote(t, repo, user.ID, "Dead Link", "dead-link", &token)

	// The deadline is moved into the past in the database, which is how time
	// passing produces this state without anybody calling the API (the API
	// refuses to create a link that is dead on arrival).
	deadline := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	res, err := pool.WriteDB.Exec(`UPDATE notes SET expires_at = ? WHERE id = ?`, deadline, note.ID)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the fixture must have expired the link it then edits")

	t.Run("extending the deadline", func(t *testing.T) {
		future := time.Now().UTC().Add(time.Hour)
		err := repo.SetShareExpiry(ctx, user.ID, note.ID, &future)
		require.Error(t, err)
		assert.ErrorIs(t, err, apperror.ErrShareExpired,
			"a future deadline on a dead link is a revival, and the repository is where it is refused")
		assert.True(t, deadline.Equal(*storedExpiry(t, pool, note.ID)),
			"a refused write must leave the deadline where it was")
	})

	t.Run("clearing the deadline", func(t *testing.T) {
		err := repo.SetShareExpiry(ctx, user.ID, note.ID, nil)
		require.Error(t, err, "nulling the deadline would make the dead link immortal")
		assert.ErrorIs(t, err, apperror.ErrShareExpired)
		assert.NotNil(t, storedExpiry(t, pool, note.ID),
			"a refused clear must leave the deadline in place, not NULL")
	})

	// Nothing above rotated the token, so the dead URL is still the dead URL.
	read, err := repo.GetByShareToken(ctx, token)
	require.NoError(t, err, "the row is still there; expiry is checked by the reader, not the lookup")
	require.NotNil(t, read.ExpiresAt)
	assert.True(t, deadline.Equal(*read.ExpiresAt), "the dead deadline must not have moved")
}

// TestNoteRepo_SetShareExpiry_LiveLinkStaysEditable is the other half of the
// rule, and the reason PATCH exists at all: on a live link, extending and
// clearing a deadline must keep working and must not rotate the token, because
// rotation would revoke every copy already in circulation.
func TestNoteRepo_SetShareExpiry_LiveLinkStaysEditable(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	token := "livelink000000000000000000000001"
	note := linkNote(t, repo, user.ID, "Live Link", "live-link", &token)

	first := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	require.NoError(t, repo.SetShareExpiry(ctx, user.ID, note.ID, &first),
		"adding a deadline to a live link must succeed")
	require.NotNil(t, storedExpiry(t, pool, note.ID))

	second := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	require.NoError(t, repo.SetShareExpiry(ctx, user.ID, note.ID, &second),
		"extending a live link's deadline must succeed without rotating the token")
	assert.True(t, second.Equal(storedExpiry(t, pool, note.ID).UTC()))

	require.NoError(t, repo.SetShareExpiry(ctx, user.ID, note.ID, nil),
		"clearing a live link's deadline must still succeed")
	assert.Nil(t, storedExpiry(t, pool, note.ID))

	read, err := repo.GetByShareToken(ctx, token)
	require.NoError(t, err)
	assert.Equal(t, token, *read.ShareToken, "no deadline edit may rotate the token")
	assert.Nil(t, read.ExpiresAt)
}

// TestNoteRepo_SetShareToken_ClearsExpiry is what makes a fresh share the way
// back from a dead link: rotating the token must drop the old deadline in the
// same statement, or the write that sets the new link's deadline would be
// refused by the very guard that makes expiry final.
func TestNoteRepo_SetShareToken_ClearsExpiry(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	first := "reshares000000000000000000000001"
	note := linkNote(t, repo, user.ID, "Reshared", "reshared", &first)

	deadline := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	_, err := pool.WriteDB.Exec(`UPDATE notes SET expires_at = ? WHERE id = ?`, deadline, note.ID)
	require.NoError(t, err)
	require.NotNil(t, storedExpiry(t, pool, note.ID))

	second := "reshares000000000000000000000002"
	require.NoError(t, repo.SetShareToken(ctx, user.ID, note.ID, &second))
	assert.Nil(t, storedExpiry(t, pool, note.ID),
		"the rotating statement must clear the previous link's deadline")

	// And the new link's deadline can then be written, which is the whole point.
	future := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	require.NoError(t, repo.SetShareExpiry(ctx, user.ID, note.ID, &future),
		"a link created by re-sharing must be editable")
	require.NotNil(t, storedExpiry(t, pool, note.ID))

	// Revoking clears it too: a note that is not shared must not carry a deadline
	// into its next link, and it must be revocable at all — expiry is final for
	// the public URL, not for the owner's ability to destroy the link.
	require.NoError(t, repo.SetShareToken(ctx, user.ID, note.ID, nil))
	assert.Nil(t, storedExpiry(t, pool, note.ID))
}

// TestNoteRepo_SetShareToken_ResetsLastViewedAt is the SQLite leg of the
// rotation rule: last_viewed_at belongs to the token that was current when the
// view happened, so the statement that moves the token must drop it.
func TestNoteRepo_SetShareToken_ResetsLastViewedAt(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	first := "rotated0000000000000000000000001"
	note := linkNote(t, repo, user.ID, "Rotated Views", "rotated-views", &first)

	viewed := time.Now().UTC()
	wrote, err := repo.TouchShareView(ctx, note.ID, nil, viewed, 10*time.Minute)
	require.NoError(t, err)
	require.True(t, wrote)
	require.True(t, storedView(t, pool, note.ID).Valid, "the fixture must have recorded a view")

	second := "rotated0000000000000000000000002"
	require.NoError(t, repo.SetShareToken(ctx, user.ID, note.ID, &second))

	assert.False(t, storedView(t, pool, note.ID).Valid,
		"a re-shared link must start with no view time rather than inherit the previous one")

	// And the new link's first genuine view is written, not suppressed by a stale
	// stamp inside the window.
	wrote, err = repo.TouchShareView(ctx, note.ID, nil, viewed.Add(time.Minute), 10*time.Minute)
	require.NoError(t, err)
	assert.True(t, wrote)

	// Revoking drops it too: a note that is not shared has no link to have views.
	require.NoError(t, repo.SetShareToken(ctx, user.ID, note.ID, nil))
	assert.False(t, storedView(t, pool, note.ID).Valid)
}
