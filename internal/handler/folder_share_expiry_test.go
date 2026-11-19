package handler_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/notes"
)

// Folder shares, slice 2 (ticket 616): the deadline.
//
// The rules under test are the ones the note-share surface already answered, and
// they are asserted here in the same shape so the two cannot drift apart:
//
//   - a deadline set at creation is stored as the instant the caller sent, and an
//     expired link answers the SAME 404 an unknown token answers, compared as
//     whole serialised responses;
//   - PATCH changes the deadline without rotating the token, so extending or
//     clearing a circulating link does not revoke it;
//   - an expired link is FINAL: both PATCH shapes are refused with 409, and the
//     only way back is a fresh share, which rotates the token;
//   - last_viewed_at is bounded by the same ten-minute window as a note share and
//     reset when the token rotates, because a view belongs to the link it came
//     through.

// storedFolderExpiry reads the deadline as the database holds it. The API echoes
// what the caller sent, so asserting on the response proves nothing about what
// was written or what a later read compares against.
func storedFolderExpiry(t *testing.T, pool *db.Pool, folderID int64) *time.Time {
	t.Helper()
	var stored *time.Time
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT expires_at FROM folder_shares WHERE folder_id = ?`, folderID).Scan(&stored))
	return stored
}

// storedFolderView reads the link's view stamp, which is what "a write happened"
// means on disk.
func storedFolderView(t *testing.T, pool *db.Pool, folderID int64) *time.Time {
	t.Helper()
	var stored *time.Time
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT last_viewed_at FROM folder_shares WHERE folder_id = ?`, folderID).Scan(&stored))
	return stored
}

// storedFolderToken reads the live token for a folder, so a write that must not
// rotate the link can be checked against the row rather than against a URL the
// test happened to keep.
func storedFolderToken(t *testing.T, pool *db.Pool, folderID int64) string {
	t.Helper()
	var token string
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT token FROM folder_shares WHERE folder_id = ?`, folderID).Scan(&token))
	return token
}

// expireFolderShare moves the link's deadline into the past.
//
// The clock is moved rather than the deadline created in the past because POST
// refuses a past deadline (that is asserted separately): a test that expired a
// link by sending a deadline the API rejects would be testing the 400, not the
// expiry rule. Everything else about the row is left exactly as the API wrote it.
func expireFolderShare(t *testing.T, pool *db.Pool, folderID int64) {
	t.Helper()
	res, err := pool.WriteDB.Exec(
		`UPDATE folder_shares SET expires_at = ? WHERE folder_id = ?`,
		time.Now().UTC().Add(-time.Hour), folderID)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the fixture must have expired the link it then reads")
}

// patchFolderExpiry performs the PATCH this slice adds and returns the raw body
// so an assertion can read what the caller was told.
func patchFolderExpiry(t *testing.T, c *testClient, folderID int64, body map[string]any) (int, string) {
	t.Helper()
	resp := c.do(t, http.MethodPatch, "/api/v1/folders/"+i64str(folderID)+"/share", body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

// folderExpiryFixture is alice's folder with one note and a link carrying a
// deadline an hour out — the state a user is in when they ask "can I extend it?".
type folderExpiryFixture struct {
	client   *testClient
	folderID int64
	token    string
}

func newFolderExpiryFixture(t *testing.T) *folderExpiryFixture {
	t.Helper()
	f := newIsolationFixture(t)
	f.asAlice(t)

	folderID := isolationCreateFolder(t, f.client, "Deadline")
	isolationCreateNote(t, f.client, &folderID, "Inside the deadline")

	token := shareFolder(t, f.client, folderID, map[string]any{
		"expires_at": time.Now().UTC().Add(time.Hour).Truncate(time.Second).Format(time.RFC3339),
	})
	return &folderExpiryFixture{client: f.client, folderID: folderID, token: token}
}

// TestFolderShareExpiry_CreateStoresTheDeadline is the write half of the POST
// contract: the deadline the caller sent is the deadline the row carries, and it
// is live until it passes.
func TestFolderShareExpiry_CreateStoresTheDeadline(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)
	folderID := isolationCreateFolder(t, f.client, "Stored deadline")
	isolationCreateNote(t, f.client, &folderID, "A note")

	want := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	token := shareFolder(t, f.client, folderID, map[string]any{
		"expires_at": want.Format(time.RFC3339),
	})

	stored := storedFolderExpiry(t, f.client.pool, folderID)
	require.NotNil(t, stored, "a POST carrying expires_at must write the deadline")
	assert.True(t, want.Equal(stored.UTC()),
		"the deadline must be stored as the instant the caller sent: want %s got %s", want, stored)

	status, body := anonBody(t, f.client, "/f/"+token)
	require.Equal(t, http.StatusOK, status, "a link with a future deadline must resolve")
	assert.Contains(t, body, "A note")
}

// TestFolderShareExpiry_PastDeadlineIsRejected: a deadline that has already
// passed would publish a link that is dead on arrival, and the caller would be
// handed a token with nothing in the response saying why it 404s.
func TestFolderShareExpiry_PastDeadlineIsRejected(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)
	folderID := isolationCreateFolder(t, f.client, "Stillborn")

	resp := f.client.do(t, http.MethodPost, "/api/v1/folders/"+i64str(folderID)+"/share",
		map[string]any{"expires_at": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)})
	rawBytes, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	raw := string(rawBytes)

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, raw, "expires_at must be in the future")

	var count int
	require.NoError(t, f.client.pool.ReadDB.QueryRow(
		`SELECT COUNT(*) FROM folder_shares WHERE folder_id = ?`, folderID).Scan(&count))
	assert.Zero(t, count, "a refused share must not leave a token behind")
}

// TestFolderShareExpiry_ExpiredLinkAnswersTheUnknownTokenResponse is the slice's
// headline acceptance: a deadline set through the API, then passed, and the link
// answers exactly what a token that never existed answers — on the listing, on a
// note read and on a subfolder listing.
//
// Whole serialised responses, not status codes: two 404s can differ in a header,
// and any difference tells the holder of a burned URL that it was real once and
// when it died.
func TestFolderShareExpiry_ExpiredLinkAnswersTheUnknownTokenResponse(t *testing.T) {
	f := newFolderExpiryFixture(t)
	f.client.login(t, "alice", isolationAlicePassword)

	const neverIssued = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	routes := []string{
		"/f/%s",
		"/f/%s/n/inside-the-deadline",
	}
	reference := make(map[string]string, len(routes))
	for _, route := range routes {
		reference[route] = serialiseResponse(t, anonGet(t, f.client, strings.ReplaceAll(route, "%s", neverIssued)))
	}

	// Live first: the comparison below must not pass because the handler 404s
	// everything under this folder.
	for _, route := range routes {
		live := serialiseResponse(t, anonGet(t, f.client, strings.ReplaceAll(route, "%s", f.token)))
		assert.NotEqual(t, reference[route], live, "the live link must answer on %s", route)
	}

	expireFolderShare(t, f.client.pool, f.folderID)

	for _, route := range routes {
		got := serialiseResponse(t, anonGet(t, f.client, strings.ReplaceAll(route, "%s", f.token)))
		assert.Equal(t, reference[route], got,
			"an expired link on %s must be indistinguishable from a token that never existed — "+
				"compared as whole responses, because an equal status is not the assertion", route)
	}
}

// TestFolderShareExpiryPatch_ExtendAndClearKeepTheToken is why PATCH exists:
// shortening or dropping a deadline must not break the URLs already handed out.
func TestFolderShareExpiryPatch_ExtendAndClearKeepTheToken(t *testing.T) {
	f := newFolderExpiryFixture(t)

	// Extending: same token, later deadline, still resolvable.
	later := time.Now().UTC().Add(6 * time.Hour).Truncate(time.Second)
	status, body := patchFolderExpiry(t, f.client, f.folderID, map[string]any{
		"expires_at": later.Format(time.RFC3339),
	})
	require.Equal(t, http.StatusOK, status, "extending a live link must succeed: %s", body)
	assert.Contains(t, body, "expires_at")

	stored := storedFolderExpiry(t, f.client.pool, f.folderID)
	require.NotNil(t, stored)
	assert.True(t, later.Equal(stored.UTC()), "the extension must be stored: want %s got %s", later, stored)
	assert.Equal(t, f.token, storedFolderToken(t, f.client.pool, f.folderID),
		"extending a deadline must not rotate the token")

	status, _ = anonBody(t, f.client, "/f/"+f.token)
	assert.Equal(t, http.StatusOK, status, "the extended link must still resolve")

	// Clearing: same token, no deadline, still resolvable.
	status, body = patchFolderExpiry(t, f.client, f.folderID, map[string]any{"clear_expiry": true})
	require.Equal(t, http.StatusOK, status, "clearing a live link's deadline must succeed: %s", body)
	assert.Contains(t, body, "expires_at")
	assert.Nil(t, storedFolderExpiry(t, f.client.pool, f.folderID),
		"clearing a live link's deadline must remove the deadline")
	assert.Equal(t, f.token, storedFolderToken(t, f.client.pool, f.folderID),
		"clearing a deadline must not rotate the token either")

	status, _ = anonBody(t, f.client, "/f/"+f.token)
	assert.Equal(t, http.StatusOK, status, "a live link with no deadline must still resolve")

	// An empty body must not be able to make anything immortal, and a past
	// deadline must not be accepted as an extension.
	status, _ = patchFolderExpiry(t, f.client, f.folderID, map[string]any{})
	assert.Equal(t, http.StatusBadRequest, status, "an empty PATCH must be refused")

	status, _ = patchFolderExpiry(t, f.client, f.folderID, map[string]any{
		"expires_at": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	})
	assert.Equal(t, http.StatusBadRequest, status, "a past deadline must be refused")

	// `clear` revokes on the POST route next door; this route must not accept the
	// same word with the opposite meaning.
	status, _ = patchFolderExpiry(t, f.client, f.folderID, map[string]any{"clear": true})
	assert.Equal(t, http.StatusBadRequest, status,
		"PATCH must not accept `clear`, which revokes on the sibling route")

	assert.Equal(t, f.token, storedFolderToken(t, f.client.pool, f.folderID),
		"no refused PATCH may rotate or revoke the link")
}

// TestFolderShareExpiryPatch_ExpiredLinkIsRefusedWith409 is the acceptance test
// for "expired is final" on the folder surface, mirroring the note route.
//
// Both write shapes are refused with 409 — not 400 (the request is well-formed
// and the caller owns the link) and not 500 (nothing failed). Clearing is
// included deliberately: making expires_at NULL does not extend a dead link, it
// makes it immortal.
func TestFolderShareExpiryPatch_ExpiredLinkIsRefusedWith409(t *testing.T) {
	f := newFolderExpiryFixture(t)
	expireFolderShare(t, f.client.pool, f.folderID)

	dead := storedFolderExpiry(t, f.client.pool, f.folderID)
	require.NotNil(t, dead)

	future := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	shapes := []struct {
		name string
		body map[string]any
	}{
		{"extending the deadline", map[string]any{"expires_at": future.Format(time.RFC3339)}},
		{"clearing the deadline", map[string]any{"clear_expiry": true}},
	}

	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			status, body := patchFolderExpiry(t, f.client, f.folderID, shape.body)
			require.Equal(t, http.StatusConflict, status,
				"an expired link must refuse a deadline edit with 409, got %d: %s", status, body)
			assert.Contains(t, body, notes.FolderShareExpiredMessage,
				"the body must explain what happened and what to do instead: %s", body)

			// The refusal stuck: the deadline is still in the past, the token has
			// not moved, and the URL is still dead. A 409 that had written anyway
			// would be worse than a 200.
			stored := storedFolderExpiry(t, f.client.pool, f.folderID)
			require.NotNil(t, stored, "a refused PATCH must leave the deadline in place")
			assert.True(t, dead.Equal(stored.UTC()), "a refused PATCH must not move the deadline: %s", stored)
			assert.Equal(t, f.token, storedFolderToken(t, f.client.pool, f.folderID))

			status, _ = anonBody(t, f.client, "/f/"+f.token)
			assert.Equal(t, http.StatusNotFound, status, "the expired URL must stay dead after a refused revival")
		})
	}

	// The one way back: a fresh share, which rotates the token. The old URL does
	// not come back with it.
	fresh := shareFolder(t, f.client, f.folderID, map[string]any{
		"expires_at": future.Format(time.RFC3339),
	})
	require.NotEqual(t, f.token, fresh, "re-sharing must rotate the token")

	status, _ := anonBody(t, f.client, "/f/"+fresh)
	require.Equal(t, http.StatusOK, status, "the new link must resolve")

	status, _ = anonBody(t, f.client, "/f/"+f.token)
	assert.Equal(t, http.StatusNotFound, status,
		"the expired URL must stay dead even after a fresh share is issued")
}

// TestFolderShareExpiryPatch_NoLinkOrNotOwnedIsNotFound: the PATCH route is
// owner-scoped, so a folder that is not shared and another user's folder answer
// the API's single not-found body — and nothing is written for either.
func TestFolderShareExpiryPatch_NoLinkOrNotOwnedIsNotFound(t *testing.T) {
	f := newIsolationFixture(t) // the session starts as bob's
	f.asAlice(t)
	sharedFolder := isolationCreateFolder(t, f.client, "Alice shared")
	unsharedFolder := isolationCreateFolder(t, f.client, "Alice unshared")
	shareFolder(t, f.client, sharedFolder, map[string]any{
		"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})

	// Back to bob for the cross-account attempt.
	f.client.login(t, "bob", isolationBobPassword)

	future := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)

	// Bob cannot edit alice's link.
	status, body := patchFolderExpiry(t, f.client, sharedFolder, map[string]any{"expires_at": future})
	assert.Equal(t, http.StatusNotFound, status, "somebody else's folder must answer not-found")
	assert.Equal(t, notFoundBody, strings.TrimSpace(body))
	assert.NotNil(t, storedFolderExpiry(t, f.client.pool, sharedFolder),
		"bob's refused write must not have cleared alice's deadline")

	f.asAlice(t)

	// Nor can alice edit a deadline on a folder that has no link: there is no
	// deadline to move, and reporting success would tell her a link now expires.
	status, body = patchFolderExpiry(t, f.client, unsharedFolder, map[string]any{"expires_at": future})
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, notFoundBody, strings.TrimSpace(body))
}

// TestFolderShareView_RecordsOneViewPerWindowAndRotationResetsIt is the counted
// bound over HTTP for the folder surface, and the rotation rule: a view belongs
// to the link it came through, so a new link must not inherit the previous one's
// stamp (which would also suppress its own first genuine view).
func TestFolderShareView_RecordsOneViewPerWindowAndRotationResetsIt(t *testing.T) {
	f := newFolderExpiryFixture(t)

	read := func(token string) {
		t.Helper()
		status, _ := anonBody(t, f.client, "/f/"+token)
		require.Equal(t, http.StatusOK, status)
	}

	read(f.token)

	// The write is asynchronous by design; wait for it rather than sleeping.
	var firstView *time.Time
	require.Eventually(t, func() bool {
		firstView = storedFolderView(t, f.client.pool, f.folderID)
		return firstView != nil
	}, 5*time.Second, 20*time.Millisecond, "a view of a live folder link must be recorded")

	// More reads inside the window must not move it.
	for i := 0; i < 4; i++ {
		read(f.token)
	}
	// Give any stray write a chance to land before asserting it did not.
	time.Sleep(200 * time.Millisecond)

	after := storedFolderView(t, f.client.pool, f.folderID)
	require.NotNil(t, after)
	assert.True(t, firstView.Equal(*after),
		"4 further reads inside the window must not write again: %s then %s", firstView, after)

	// Re-sharing rotates the token and resets the stamp with it.
	second := shareFolder(t, f.client, f.folderID, nil)
	require.NotEqual(t, f.token, second, "re-sharing must issue a different link")
	assert.Nil(t, storedFolderView(t, f.client.pool, f.folderID),
		"a link created seconds ago must not report the previous link's view time")

	// And the new link's first real view is recorded rather than suppressed by
	// the old link's stamp.
	read(second)
	var freshView *time.Time
	require.Eventually(t, func() bool {
		freshView = storedFolderView(t, f.client.pool, f.folderID)
		return freshView != nil
	}, 5*time.Second, 20*time.Millisecond,
		"the new link's first view must be recorded, not suppressed by the previous link's stamp")
}
