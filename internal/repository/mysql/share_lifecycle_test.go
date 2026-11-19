package mysql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
)

// The MariaDB leg of the share-link lifecycle. It mirrors the SQLite suite on
// purpose: the two repositories carry the same statements but not the same
// driver semantics, and the touch bound in particular leans on how each engine
// counts rows and compares a DATETIME, so "it works on SQLite" is not evidence
// for this one.

// mysqlLinkNote inserts a note with its own slug and, optionally, a share token.
func mysqlLinkNote(t *testing.T, repo *NoteRepo, userID int64, title, slug string, token *string) *model.Note {
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
	}
	return created
}

func mysqlStoredView(t *testing.T, pool *db.Pool, noteID int64) *time.Time {
	t.Helper()
	var v *time.Time
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT last_viewed_at FROM notes WHERE id = ?`, noteID).Scan(&v))
	return v
}

// TestMySQL_NoteRepo_TouchShareView_CountedBound is the MariaDB acceptance test
// for the touch bound: five requests inside the window, one write; the first
// request outside it, the second.
//
// Times are truncated to the second because the column is DATETIME without
// fractional precision — the bound is minutes wide, so second granularity is not
// a limitation, but an assertion on sub-second values would fail for a reason
// that has nothing to do with the bound.
func TestMySQL_NoteRepo_TouchShareView_CountedBound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)
	ctx := context.Background()

	token := "counted0000000000000000000000001"
	note := mysqlLinkNote(t, repo, user.ID, "Counted Views", "counted-views", &token)

	const window = 10 * time.Minute
	start := time.Now().UTC().Truncate(time.Second)

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

	stored := mysqlStoredView(t, pool, note.ID)
	require.NotNil(t, stored, "the first view must have been recorded")
	assert.True(t, start.Equal(stored.UTC()),
		"the stored value must be the first view in the window (got %s, want %s)", stored.UTC(), start)

	outside := start.Add(window + time.Minute)
	current, err := repo.GetByShareToken(ctx, token)
	require.NoError(t, err)
	wrote, err := repo.TouchShareView(ctx, note.ID, current.LastViewedAt, outside, window)
	require.NoError(t, err)
	require.True(t, wrote, "the first request outside the window must write")
	writes++

	assert.Equal(t, 2, writes, "one write per window, not one per request")

	stored = mysqlStoredView(t, pool, note.ID)
	require.NotNil(t, stored)
	assert.True(t, outside.Equal(stored.UTC()),
		"the second write must carry the first timestamp outside the window")
}

// TestMySQL_NoteRepo_ListShares_RedactsToken is the MariaDB face of the
// enumeration payload: the caller's own links only, token cut to a prefix.
func TestMySQL_NoteRepo_ListShares_RedactsToken(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	other, err := NewUserRepo(pool.WriteDB).Create(context.Background(), "other", "hash", "uuid-other")
	require.NoError(t, err)

	repo := NewNoteRepo(pool.WriteDB)
	ctx := context.Background()

	tokenA := "0123456789abcdef0123456789abcdef"
	tokenB := "fedcba9876543210fedcba9876543210"
	foreign := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	alpha := mysqlLinkNote(t, repo, user.ID, "Alpha", "alpha", &tokenA)
	beta := mysqlLinkNote(t, repo, user.ID, "Beta", "beta", &tokenB)
	mysqlLinkNote(t, repo, user.ID, "Private", "private", nil)
	mysqlLinkNote(t, repo, other.ID, "Foreign", "foreign", &foreign)

	deadline := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	require.NoError(t, repo.SetShareExpiry(ctx, user.ID, alpha.ID, &deadline))
	touched, err := repo.TouchShareView(ctx, alpha.ID, nil, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, touched)

	shares, err := repo.ListShares(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, shares, 2)

	byNote := map[int64]model.ShareLink{}
	for _, s := range shares {
		require.NotNil(t, s.NoteID, "a note row carries its note id")
		assert.Equal(t, model.ShareKindNote, s.Kind)
		byNote[*s.NoteID] = s
		assert.NotEqual(t, tokenA, s.TokenPrefix)
		assert.NotEqual(t, tokenB, s.TokenPrefix)
	}

	a, ok := byNote[alpha.ID]
	require.True(t, ok)
	assert.Equal(t, tokenA[:8]+"…", a.TokenPrefix)
	require.NotNil(t, a.ExpiresAt)
	require.NotNil(t, a.LastViewedAt)

	b, ok := byNote[beta.ID]
	require.True(t, ok)
	assert.Equal(t, tokenB[:8]+"…", b.TokenPrefix)
	assert.Nil(t, b.ExpiresAt, "no expiry must read as NULL, not as a zero time")

	// SetShareExpiry is the changed-rows case on this driver: writing the same
	// deadline twice changes nothing, and MySQL reports zero rows for it. It must
	// still be success, not "not your note".
	require.NoError(t, repo.SetShareExpiry(ctx, user.ID, alpha.ID, &deadline),
		"re-setting an unchanged deadline must be a no-op success, not not-found")
	var expiresAt *time.Time
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT expires_at FROM notes WHERE id = ?`, alpha.ID).Scan(&expiresAt))
	require.NotNil(t, expiresAt)
}

// TestMySQL_NoteRepo_SetShareExpiry_UnsharedNoteIsNotFound pins the guard: a
// deadline on a note with no link is unreachable state and must be rejected on
// this engine too.
func TestMySQL_NoteRepo_SetShareExpiry_UnsharedNoteIsNotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)
	note := mysqlLinkNote(t, repo, user.ID, "No Link", "no-link", nil)

	deadline := time.Now().Add(time.Hour)
	err := repo.SetShareExpiry(context.Background(), user.ID, note.ID, &deadline)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

// mysqlStoredExpiry reads the deadline as the row holds it. A refused write is
// proved by this value being unchanged, not by the error alone: an error plus a
// written column would be the worst of both.
func mysqlStoredExpiry(t *testing.T, pool *db.Pool, noteID int64) *time.Time {
	t.Helper()
	var expiresAt *time.Time
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT expires_at FROM notes WHERE id = ?`, noteID).Scan(&expiresAt))
	return expiresAt
}

// TestMySQL_NoteRepo_SetShareExpiry_ExpiredLinkIsRefused is the MariaDB leg of
// "expired is final", for both write shapes.
//
// It is a separate test from the SQLite one on purpose: the guard is a
// comparison between a bound time and a DATETIME column inside an UPDATE, and the
// two engines do not answer that question the same way — MariaDB truncates the
// bound value to the column's second precision, SQLite compares the text form its
// driver stores — so "the SQLite suite proves it" is not evidence here.
func TestMySQL_NoteRepo_SetShareExpiry_ExpiredLinkIsRefused(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)
	ctx := context.Background()

	token := "expired0000000000000000000000001"
	note := mysqlLinkNote(t, repo, user.ID, "Dead Link", "dead-link", &token)

	deadline := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	res, err := pool.WriteDB.Exec(`UPDATE notes SET expires_at = ? WHERE id = ?`, deadline, note.ID)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the fixture must have expired the link it then edits")

	t.Run("extending the deadline", func(t *testing.T) {
		future := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
		err := repo.SetShareExpiry(ctx, user.ID, note.ID, &future)
		require.Error(t, err)
		assert.ErrorIs(t, err, apperror.ErrShareExpired,
			"on a dead link the guard must win over the driver's changed-rows answer")
		stored := mysqlStoredExpiry(t, pool, note.ID)
		require.NotNil(t, stored)
		assert.True(t, deadline.Equal(stored.UTC()), "a refused write must leave the deadline alone")
	})

	t.Run("clearing the deadline", func(t *testing.T) {
		err := repo.SetShareExpiry(ctx, user.ID, note.ID, nil)
		require.Error(t, err, "nulling the deadline would make the dead link immortal")
		assert.ErrorIs(t, err, apperror.ErrShareExpired)
		assert.NotNil(t, mysqlStoredExpiry(t, pool, note.ID),
			"a refused clear must leave the deadline in place, not NULL")
	})
}

// TestMySQL_NoteRepo_SetShareExpiry_LiveLinkStaysEditable keeps the other half:
// a live link's deadline can still be added, extended and cleared, and the token
// never moves. The unchanged-deadline case is asserted too, because on this
// driver "changed no rows" is also what re-writing the same value looks like and
// that must not be read as a refusal.
func TestMySQL_NoteRepo_SetShareExpiry_LiveLinkStaysEditable(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)
	ctx := context.Background()

	token := "livelink000000000000000000000001"
	note := mysqlLinkNote(t, repo, user.ID, "Live Link", "live-link", &token)

	first := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	require.NoError(t, repo.SetShareExpiry(ctx, user.ID, note.ID, &first),
		"adding a deadline to a live link must succeed")
	require.NotNil(t, mysqlStoredExpiry(t, pool, note.ID))

	require.NoError(t, repo.SetShareExpiry(ctx, user.ID, note.ID, &first),
		"re-setting an unchanged deadline must stay a success, not a refusal")

	second := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	require.NoError(t, repo.SetShareExpiry(ctx, user.ID, note.ID, &second),
		"extending a live link's deadline must succeed without rotating the token")
	stored := mysqlStoredExpiry(t, pool, note.ID)
	require.NotNil(t, stored)
	assert.True(t, second.Equal(stored.UTC()))

	require.NoError(t, repo.SetShareExpiry(ctx, user.ID, note.ID, nil),
		"clearing a live link's deadline must still succeed")
	assert.Nil(t, mysqlStoredExpiry(t, pool, note.ID))

	read, err := repo.GetByShareToken(ctx, token)
	require.NoError(t, err)
	assert.Equal(t, token, *read.ShareToken, "no deadline edit may rotate the token")
	assert.Nil(t, read.ExpiresAt)
}

// TestMySQL_NoteRepo_SetShareToken_ClearsExpiry: the rotating statement must drop
// the previous deadline, or a fresh share — the only way back from a dead link —
// would be born dead and refused.
func TestMySQL_NoteRepo_SetShareToken_ClearsExpiry(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)
	ctx := context.Background()

	first := "reshares000000000000000000000001"
	note := mysqlLinkNote(t, repo, user.ID, "Reshared", "reshared", &first)

	deadline := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	_, err := pool.WriteDB.Exec(`UPDATE notes SET expires_at = ? WHERE id = ?`, deadline, note.ID)
	require.NoError(t, err)
	require.NotNil(t, mysqlStoredExpiry(t, pool, note.ID))

	second := "reshares000000000000000000000002"
	require.NoError(t, repo.SetShareToken(ctx, user.ID, note.ID, &second))
	assert.Nil(t, mysqlStoredExpiry(t, pool, note.ID),
		"the rotating statement must clear the previous link's deadline")

	future := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	require.NoError(t, repo.SetShareExpiry(ctx, user.ID, note.ID, &future),
		"a link created by re-sharing must be editable")

	// Revoking clears the deadline with the token, and must work on any link.
	require.NoError(t, repo.SetShareToken(ctx, user.ID, note.ID, nil))
	assert.Nil(t, mysqlStoredExpiry(t, pool, note.ID))
}

// TestMySQL_NoteRepo_SetShareToken_ResetsLastViewedAt is the MariaDB leg of the
// rotation rule. It is separate from the SQLite test on purpose: the two
// repositories reach "zero rows affected" differently (updateScoped's existence
// predicate versus a matched-row count), and the reset rides on that statement.
func TestMySQL_NoteRepo_SetShareToken_ResetsLastViewedAt(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)
	ctx := context.Background()

	first := "rotated0000000000000000000000001"
	note := mysqlLinkNote(t, repo, user.ID, "Rotated Views", "rotated-views", &first)

	viewed := time.Now().UTC().Truncate(time.Second)
	wrote, err := repo.TouchShareView(ctx, note.ID, nil, viewed, 10*time.Minute)
	require.NoError(t, err)
	require.True(t, wrote)
	require.NotNil(t, mysqlStoredView(t, pool, note.ID), "the fixture must have recorded a view")

	second := "rotated0000000000000000000000002"
	require.NoError(t, repo.SetShareToken(ctx, user.ID, note.ID, &second))

	assert.Nil(t, mysqlStoredView(t, pool, note.ID),
		"a re-shared link must start with no view time rather than inherit the previous one")

	wrote, err = repo.TouchShareView(ctx, note.ID, nil, viewed.Add(time.Minute), 10*time.Minute)
	require.NoError(t, err)
	assert.True(t, wrote, "the new link's first view must be recorded")

	// Revoking drops it too.
	require.NoError(t, repo.SetShareToken(ctx, user.ID, note.ID, nil))
	assert.Nil(t, mysqlStoredView(t, pool, note.ID))
}

// TestMySQL_NoteRepo_ShareExpiryOffsetRoundTrips records why the UTC
// normalisation lives in the service: MariaDB stores a DATETIME and reads the
// same instant back whatever offset the caller used, so the poisoning security
// review measured is a SQLite-driver defect — and SQLite is the default driver.
// Asserting it here keeps that claim checkable instead of remembered.
func TestMySQL_NoteRepo_ShareExpiryOffsetRoundTrips(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)
	ctx := context.Background()

	token := "offsets0000000000000000000000001"
	note := mysqlLinkNote(t, repo, user.ID, "Offset Deadline", "offset-deadline", &token)

	sent := time.Date(2026, 11, 19, 5, 48, 54, 0, time.FixedZone("+05:00", 5*60*60))
	require.NoError(t, repo.SetShareExpiry(ctx, user.ID, note.ID, &sent))

	read, err := repo.GetByShareToken(ctx, token)
	require.NoError(t, err, "a deadline with an offset must not break reading the note")
	require.NotNil(t, read.ExpiresAt)
	assert.True(t, read.ExpiresAt.Equal(sent),
		"stored instant %s must be the instant that was written %s", read.ExpiresAt, sent)

	shares, err := repo.ListShares(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, shares, 1)
	require.NotNil(t, shares[0].ExpiresAt)
	assert.True(t, shares[0].ExpiresAt.Equal(sent), "the list must report the same instant")
}
