package handler_test

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
)

// Share-link lifecycle over HTTP (ticket 601, item 3).
//
// Three properties are pinned here, and each one is a thing that is easy to get
// wrong in a way no status-code assertion would catch:
//
//   - an expired link is indistinguishable from one that never existed, compared
//     as whole serialised responses rather than as status codes;
//   - the view stamp is bounded, so a read-heavy anonymous route does not become
//     a write per request;
//   - the caller can enumerate their own links, with the token redacted — which
//     is what makes bulk revocation possible at all.

// shareNote creates a share link and returns its token.
func shareNote(t *testing.T, c *testClient, noteID int64, body map[string]any) string {
	t.Helper()
	resp := c.do(t, http.MethodPost, "/api/v1/notes/"+i64str(noteID)+"/share", body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out map[string]any
	decodeInto(t, resp, &out)
	token, ok := out["share_token"].(string)
	require.True(t, ok, "sharing must return a token, got %v", out["share_token"])
	require.NotEmpty(t, token)
	return token
}

// expireShare moves an existing link's deadline into the past, which is how a
// test travels through time without sleeping. The API refuses a past deadline
// (that is asserted separately), so the clock is moved in the database instead.
func expireShare(t *testing.T, pool *db.Pool, noteID int64) {
	t.Helper()
	past := time.Now().UTC().Add(-time.Hour)
	res, err := pool.WriteDB.Exec(`UPDATE notes SET expires_at = ? WHERE id = ?`, past, noteID)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the fixture must have set the deadline it then expired")
}

// serialiseResponse renders a whole HTTP response — status line, headers and
// body — into one comparable string.
//
// Date is dropped because two identical responses one second apart differ there
// and nowhere else: comparing it would make the assertion flaky rather than
// strict. Every other header is included, so a handler that adds (say) an
// explanation header only on the expired branch fails the comparison.
func serialiseResponse(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	var b strings.Builder
	fmt.Fprintf(&b, "%s %d\n", resp.Proto, resp.StatusCode)

	keys := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		if strings.EqualFold(k, "Date") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(resp.Header.Values(k), ", "))
	}
	fmt.Fprintf(&b, "\n%s", body)
	return b.String()
}

// TestShareView_HeaderProbes covers the live, expired and nonexistent probes the
// ticket asks for: all three carry no-store and noindex, and the expired and
// nonexistent responses are byte-identical once the clock-dependent Date header
// is set aside.
func TestShareView_HeaderProbes(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	liveToken := shareNote(t, f.client, f.noteID, nil)

	expiringNote := isolationCreateNote(t, f.client, nil, "Expiring note")
	expiredToken := shareNote(t, f.client, expiringNote, map[string]any{
		"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})
	expireShare(t, f.client.pool, expiringNote)

	const missingToken = "ffffffffffffffffffffffffffffffff"

	t.Run("live link", func(t *testing.T) {
		resp := f.client.do(t, http.MethodGet, "/s/"+liveToken, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
		assert.Equal(t, "noindex", resp.Header.Get("X-Robots-Tag"))
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Contains(t, string(body), "Alice private note", "the live link must actually serve the note")
	})

	t.Run("expired link", func(t *testing.T) {
		resp := f.client.do(t, http.MethodGet, "/s/"+expiredToken, nil)
		defer resp.Body.Close()
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"),
			"a dead link is still a capability: nothing may cache its answer")
		assert.Equal(t, "noindex", resp.Header.Get("X-Robots-Tag"))
	})

	t.Run("nonexistent token", func(t *testing.T) {
		resp := f.client.do(t, http.MethodGet, "/s/"+missingToken, nil)
		defer resp.Body.Close()
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
		assert.Equal(t, "noindex", resp.Header.Get("X-Robots-Tag"))
	})
}

// TestShareView_ExpiredIsIndistinguishableFromMissing is the 469 precedent
// applied to expiry: the assertion is on the whole serialised response, not on
// the status code. A body, a header or a redirect that only the expired case
// carries would tell a caller that a token was real once and when it died, which
// is exactly what a burned link must not reveal.
func TestShareView_ExpiredIsIndistinguishableFromMissing(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	expiringNote := isolationCreateNote(t, f.client, nil, "Expiring note")
	expiredToken := shareNote(t, f.client, expiringNote, map[string]any{
		"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})
	expireShare(t, f.client.pool, expiringNote)

	// A token of the same shape that was never issued: a random 32 hex string,
	// as the real ones are.
	const neverIssued = "0123456789abcdef0123456789abcdef"
	require.NotEqual(t, expiredToken, neverIssued)

	expired := serialiseResponse(t, f.client.do(t, http.MethodGet, "/s/"+expiredToken, nil))
	missing := serialiseResponse(t, f.client.do(t, http.MethodGet, "/s/"+neverIssued, nil))

	assert.Equal(t, missing, expired,
		"an expired link must be indistinguishable from a token that never existed — "+
			"compared as whole responses, because an equal status code is not the assertion")

	// Positive control: the live link is NOT in this equivalence class, so the
	// comparison above cannot pass by the handler 404ing everything.
	liveToken := shareNote(t, f.client, f.noteID, nil)
	live := serialiseResponse(t, f.client.do(t, http.MethodGet, "/s/"+liveToken, nil))
	assert.NotEqual(t, missing, live)
}

// TestShareView_ExpiredLinkStillResolvesForItsOwner checks the other side of the
// same decision: expiry removes the PUBLIC answer only. The owner still sees
// their note and still sees it in their share list, because a link that stopped
// working is not a note that went away.
func TestShareView_ExpiredLinkStillResolvesForItsOwner(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	noteID := isolationCreateNote(t, f.client, nil, "Expiring note")
	token := shareNote(t, f.client, noteID, map[string]any{
		"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})
	expireShare(t, f.client.pool, noteID)

	resp := f.client.do(t, http.MethodGet, "/api/v1/notes/"+i64str(noteID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	shares := listShares(t, f.client)
	require.Len(t, shares, 1)
	assert.Equal(t, noteID, shares[0].NoteID)
	assert.NotNil(t, shares[0].ExpiresAt, "the dead deadline must still be visible, or it cannot be revoked")

	resp = f.client.do(t, http.MethodGet, "/s/"+token, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

// listShares reads GET /api/v1/shares.
func listShares(t *testing.T, c *testClient) []model.ShareLink {
	t.Helper()
	resp := c.do(t, http.MethodGet, "/api/v1/shares", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var shares []model.ShareLink
	decodeInto(t, resp, &shares)
	return shares
}

// TestShareList_OwnSharesWithRedactedToken is the acceptance test for the
// enumeration endpoint: it lists the caller's own links, and it cannot be used
// to harvest a working token.
func TestShareList_OwnSharesWithRedactedToken(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	firstToken := shareNote(t, f.client, f.noteID, nil)
	secondNote := isolationCreateNote(t, f.client, nil, "Second shared note")
	secondToken := shareNote(t, f.client, secondNote, map[string]any{
		"expires_at": time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339),
	})

	shares := listShares(t, f.client)
	require.Len(t, shares, 2, "both of alice's shared notes, and only those")

	byNote := map[int64]model.ShareLink{}
	for _, s := range shares {
		byNote[s.NoteID] = s
		// The token itself must never appear in this payload: one authenticated
		// read must not be a bulk export of every share.
		assert.NotEqual(t, firstToken, s.TokenPrefix)
		assert.NotEqual(t, secondToken, s.TokenPrefix)
		assert.Len(t, []rune(s.TokenPrefix), 9, "a fixed-width prefix plus an ellipsis")
		assert.True(t, strings.HasSuffix(s.TokenPrefix, "…"))
	}

	first, ok := byNote[f.noteID]
	require.True(t, ok)
	assert.Equal(t, "Alice private note", first.Title)
	assert.Equal(t, firstToken[:8]+"…", first.TokenPrefix)
	assert.Nil(t, first.ExpiresAt, "no expiry is the default and must read as null")

	second, ok := byNote[secondNote]
	require.True(t, ok)
	assert.Equal(t, secondToken[:8]+"…", second.TokenPrefix)
	require.NotNil(t, second.ExpiresAt, "an opt-in deadline must be visible here")

	// The second account sees its own (empty) list, never alice's.
	f.client.login(t, "bob", isolationBobPassword)
	assert.Empty(t, listShares(t, f.client), "the list is scoped to the caller's own links")

	// Unauthenticated callers get nothing at all. A fresh client without the
	// session jar, because the jar is what carries the cookie the endpoint reads.
	f.client.cookies = nil
	f.client.csrfToken = ""
	anon := &http.Client{}
	req, err := http.NewRequest(http.MethodGet, f.client.server.URL+"/api/v1/shares", nil)
	require.NoError(t, err)
	resp, err := anon.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestShareCreate_ExpiryIsOptIn pins the default: sharing without a deadline
// keeps the link immortal, which is what every existing link already is.
func TestShareCreate_ExpiryIsOptIn(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	token := shareNote(t, f.client, f.noteID, nil)

	var expiresAt *time.Time
	require.NoError(t, f.client.pool.ReadDB.QueryRow(
		`SELECT expires_at FROM notes WHERE id = ?`, f.noteID).Scan(&expiresAt))
	assert.Nil(t, expiresAt, "sharing with no expires_at must leave the link without one")

	resp := f.client.do(t, http.MethodGet, "/s/"+token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

// TestShareCreate_PastDeadlineIsRejected: a deadline in the past would publish a
// link that is dead on arrival. The caller must be told, not handed a token.
func TestShareCreate_PastDeadlineIsRejected(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	resp := f.client.do(t, http.MethodPost, "/api/v1/notes/"+i64str(f.noteID)+"/share",
		map[string]any{"expires_at": past})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var token *string
	require.NoError(t, f.client.pool.ReadDB.QueryRow(
		`SELECT share_token FROM notes WHERE id = ?`, f.noteID).Scan(&token))
	assert.Nil(t, token, "a rejected request must not leave a token behind")
}

// TestShareExpiryPatch_ChangesDeadlineWithoutRotatingToken is why PATCH exists:
// shortening or dropping a deadline must not break the URLs already handed out.
func TestShareExpiryPatch_ChangesDeadlineWithoutRotatingToken(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	token := shareNote(t, f.client, f.noteID, nil)
	path := "/api/v1/notes/" + i64str(f.noteID) + "/share"

	// Add a deadline to the live link.
	deadline := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	resp := f.client.do(t, http.MethodPatch, path, map[string]any{
		"expires_at": deadline.Format(time.RFC3339),
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// The token did not move, and it still resolves.
	var stored *string
	require.NoError(t, f.client.pool.ReadDB.QueryRow(
		`SELECT share_token FROM notes WHERE id = ?`, f.noteID).Scan(&stored))
	require.NotNil(t, stored)
	assert.Equal(t, token, *stored, "changing a deadline must not rotate the token")

	resp = f.client.do(t, http.MethodGet, "/s/"+token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// And it takes effect: expire it via the DB and the link stops answering.
	expireShare(t, f.client.pool, f.noteID)
	resp = f.client.do(t, http.MethodGet, "/s/"+token, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	// Clearing the deadline makes the same token immortal again.
	resp = f.client.do(t, http.MethodPatch, path, map[string]any{"clear": true})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = f.client.do(t, http.MethodGet, "/s/"+token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "clearing the deadline must revive the same link")
	resp.Body.Close()
}

// TestShareExpiryPatch_RequiresAnInstruction: an empty body must not be able to
// silently make a link immortal.
func TestShareExpiryPatch_RequiresAnInstruction(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)
	shareNote(t, f.client, f.noteID, nil)

	resp := f.client.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(f.noteID)+"/share",
		map[string]any{})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestShareView_RecordsOneViewPerWindow is the counted bound over HTTP: several
// reads of a live link record one view, and the write happens off the request
// path (so it is awaited here rather than assumed).
func TestShareView_RecordsOneViewPerWindow(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	token := shareNote(t, f.client, f.noteID, nil)

	readNote := func() {
		resp := f.client.do(t, http.MethodGet, "/s/"+token, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
	}

	readNote()

	// The write is asynchronous by design; wait for it rather than sleeping a
	// fixed amount and hoping.
	var firstView *time.Time
	require.Eventually(t, func() bool {
		require.NoError(t, f.client.pool.ReadDB.QueryRow(
			`SELECT last_viewed_at FROM notes WHERE id = ?`, f.noteID).Scan(&firstView))
		return firstView != nil
	}, 5*time.Second, 20*time.Millisecond, "a view of a live link must be recorded")

	// More reads inside the window must not move it.
	for i := 0; i < 4; i++ {
		readNote()
	}

	// Give any stray write a chance to land before asserting it did not.
	time.Sleep(200 * time.Millisecond)

	var after *time.Time
	require.NoError(t, f.client.pool.ReadDB.QueryRow(
		`SELECT last_viewed_at FROM notes WHERE id = ?`, f.noteID).Scan(&after))
	require.NotNil(t, after)
	assert.True(t, firstView.Equal(*after),
		"4 further reads inside the window must not write again: %s then %s", firstView, after)
}
