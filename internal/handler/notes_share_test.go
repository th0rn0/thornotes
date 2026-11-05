package handler_test

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storedShareToken reads the real share_token column rather than the API's view
// of it. The defect these tests pin down was a success response for an UPDATE
// that wrote nothing, so the database — not the response body — is the source of
// truth for "did sharing actually happen".
func storedShareToken(t *testing.T, c *testClient, noteID int64) sql.NullString {
	t.Helper()
	var token sql.NullString
	require.NoError(t, c.pool.ReadDB.QueryRow(
		`SELECT share_token FROM notes WHERE id = ?`, noteID).Scan(&token))
	return token
}

// TestShare_OtherUsersNote_Returns404: sharing a note owned by another user must
// answer 404 and must leave the owner's row untouched.
//
// Before the fix internal/repository/sqlite/notes.go SetShareToken ran
// `UPDATE ... WHERE id = ? AND user_id = ?` and discarded RowsAffected, so a
// zero-row update looked like success: the handler returned 200 with a freshly
// generated token that was never written, and the caller was told the note was
// shared when it was not.
func TestShare_OtherUsersNote_Returns404(t *testing.T) {
	f := newIsolationFixture(t) // fixture leaves the session on bob; the note is alice's

	// Alice shares her own note first, so bob's attempt has a live token to clobber.
	f.asAlice(t)
	resp := f.client.do(t, http.MethodPost, "/api/v1/notes/"+i64str(f.noteID)+"/share", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var ownerShare map[string]any
	decodeInto(t, resp, &ownerShare)
	ownerToken, ok := ownerShare["share_token"].(string)
	require.True(t, ok, "the owner must still be able to share")
	require.NotEmpty(t, ownerToken)
	require.Equal(t, ownerToken, storedShareToken(t, f.client, f.noteID).String)

	// Bob tries to share alice's note.
	f.client.login(t, "bob", isolationBobPassword)
	resp = f.client.do(t, http.MethodPost, "/api/v1/notes/"+i64str(f.noteID)+"/share", map[string]any{})
	require.Equal(t, http.StatusNotFound, resp.StatusCode,
		"sharing another user's note must answer 404, never 200 with a token")
	var body map[string]any
	decodeInto(t, resp, &body)
	assert.Contains(t, body, "error")
	assert.NotContains(t, body, "share_token", "a 404 must not hand out a share token")

	// The column still holds alice's token: bob's UPDATE matched no row, so
	// nothing was written and nothing was rotated.
	assert.Equal(t, ownerToken, storedShareToken(t, f.client, f.noteID).String,
		"alice's share_token must be unchanged after bob's attempt")
}

// TestShare_OwnNote_StillWorks is the positive control: the owner shares their own
// note, gets 200, and the token in the response is the token that reached the
// column. Without it, a blanket 404 would satisfy the negative test above and the
// fix would have simply broken sharing.
func TestShare_OwnNote_StillWorks(t *testing.T) {
	c := newTestClient(t)
	isolationLogin(t, c, "alice")
	noteID := isolationCreateNote(t, c, nil, "Alice shares this one")

	resp := c.do(t, http.MethodPost, "/api/v1/notes/"+i64str(noteID)+"/share", map[string]any{})
	require.Equal(t, http.StatusOK, resp.StatusCode, "the owner must be able to share")
	var share map[string]any
	decodeInto(t, resp, &share)
	token, ok := share["share_token"].(string)
	require.True(t, ok, "success body must still carry share_token")
	require.NotEmpty(t, token)

	stored := storedShareToken(t, c, noteID)
	require.True(t, stored.Valid, "sharing must persist a token")
	assert.Equal(t, token, stored.String, "the returned token must be the one persisted")

	// Unsharing is the same endpoint with clear=true; it must still work and must
	// NULL the column rather than reporting a no-op.
	resp = c.do(t, http.MethodPost, "/api/v1/notes/"+i64str(noteID)+"/share",
		map[string]any{"clear": true})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var cleared map[string]any
	decodeInto(t, resp, &cleared)
	assert.Nil(t, cleared["share_token"])
	assert.False(t, storedShareToken(t, c, noteID).Valid, "clearing must NULL the column")
}

// doRaw sends an already-encoded body. The malformed-unshare cases must reach
// the handler as the exact bytes a client would send, so they cannot go through
// do(), which json.Marshal()s its argument and would turn "not-json" into a
// valid JSON string.
func (c *testClient) doRaw(t *testing.T, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, c.server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}
	if c.csrfToken != "" && method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		req.Header.Set("X-CSRF-Token", c.csrfToken)
	}
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	return resp
}

// shareTokenOf decodes a share response and returns its token (nil when the
// body says the note is no longer shared).
func shareTokenOf(t *testing.T, resp *http.Response) *string {
	t.Helper()
	var body map[string]any
	decodeInto(t, resp, &body)
	require.Contains(t, body, "share_token", "every /share response must carry share_token")
	raw, present := body["share_token"]
	require.True(t, present)
	if raw == nil {
		return nil
	}
	token, ok := raw.(string)
	require.True(t, ok, "share_token must be a string or null")
	return &token
}

// shareViewStatus fetches the public link for token and returns its status.
func shareViewStatus(t *testing.T, c *testClient, token string) int {
	t.Helper()
	resp, err := c.httpClient.Get(c.server.URL + "/s/" + token)
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestShare_UnsharePayloads pins the whole unshare contract against the real
// route: a well-formed clear unpublishes, and every payload the handler cannot
// decode is a 400 that changes nothing.
//
// The defect this covers: the handler discarded the decode error from the
// request body, so {"clear":"true"}, {"clear":1} and a non-JSON body all fell
// through to the generate branch. A user who tried to unpublish with a typo was
// told it worked and handed a fresh, working public link. The column assertion
// is the point — a 400 that had already rotated or revoked the token would still
// have answered 400.
func TestShare_UnsharePayloads(t *testing.T) {
	c := newTestClient(t)
	isolationLogin(t, c, "alice")
	noteID := isolationCreateNote(t, c, nil, "Unshare payload note")
	sharePath := "/api/v1/notes/" + i64str(noteID) + "/share"

	// 1. share (valid) → 200 with a token that reaches the column.
	resp := c.do(t, http.MethodPost, sharePath, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	first := shareTokenOf(t, resp)
	require.NotNil(t, first)
	require.Equal(t, *first, storedShareToken(t, c, noteID).String)
	require.Equal(t, http.StatusOK, shareViewStatus(t, c, *first), "a fresh share link must work")

	// 2. unshare {"clear":true} → 200 null, and the public link stops working.
	resp = c.do(t, http.MethodPost, sharePath, map[string]any{"clear": true})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Nil(t, shareTokenOf(t, resp))
	assert.False(t, storedShareToken(t, c, noteID).Valid, "clear must NULL the column")
	assert.Equal(t, http.StatusNotFound, shareViewStatus(t, c, *first),
		"the cleared link must stop working")

	// 3. re-share (valid) → 200 with a second, different token.
	resp = c.do(t, http.MethodPost, sharePath, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	second := shareTokenOf(t, resp)
	require.NotNil(t, second)
	require.NotEqual(t, *first, *second, "re-sharing after a clear must mint a new token")
	require.Equal(t, *second, storedShareToken(t, c, noteID).String)

	// 4. Every malformed unshare is a 400 that leaves that token alone: not
	// revoked, not rotated, and the live public link still resolves.
	for _, tc := range []struct {
		name string
		body string
	}{
		{"clear_as_string", `{"clear":"true"}`},
		{"clear_as_number", `{"clear":1}`},
		{"not_json", `not-json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := c.doRaw(t, http.MethodPost, sharePath, tc.body)
			defer bad.Body.Close()
			require.Equal(t, http.StatusBadRequest, bad.StatusCode,
				"an unreadable request must never be interpreted as %q", tc.body)

			var body map[string]any
			decodeInto(t, bad, &body)
			assert.Contains(t, body, "error", "a 400 must explain the refused request")
			assert.NotContains(t, body, "share_token",
				"a refused unshare must not hand back a link")

			stored := storedShareToken(t, c, noteID)
			require.True(t, stored.Valid, "a 400 must not revoke the existing token")
			assert.Equal(t, *second, stored.String, "a 400 must not rotate the existing token")
			assert.Equal(t, http.StatusOK, shareViewStatus(t, c, *second),
				"the existing public link must still work after a refused request")
		})
	}

	// And a well-formed clear still works after all of that.
	resp = c.do(t, http.MethodPost, sharePath, map[string]any{"clear": true})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Nil(t, shareTokenOf(t, resp))
	assert.False(t, storedShareToken(t, c, noteID).Valid)
}

// TestShare_ClearTwice_SecondResponseIs200Null covers the changed-rows case on
// the endpoint: clearing an already-unshared note is a no-op, not an error and
// not a fresh token. The repository test proves the write returns nil; this one
// proves the caller sees 200 with a null token rather than a 404 or a 200 with a
// link.
func TestShare_ClearTwice_SecondResponseIs200Null(t *testing.T) {
	c := newTestClient(t)
	isolationLogin(t, c, "alice")
	noteID := isolationCreateNote(t, c, nil, "Clear twice via the API")
	sharePath := "/api/v1/notes/" + i64str(noteID) + "/share"

	resp := c.do(t, http.MethodPost, sharePath, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotNil(t, shareTokenOf(t, resp))

	for attempt := 1; attempt <= 2; attempt++ {
		resp = c.do(t, http.MethodPost, sharePath, map[string]any{"clear": true})
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"clear attempt %d must answer 200", attempt)
		assert.Nil(t, shareTokenOf(t, resp), "clear attempt %d must answer null", attempt)
		assert.False(t, storedShareToken(t, c, noteID).Valid,
			"clear attempt %d must leave the column NULL", attempt)
	}
}
