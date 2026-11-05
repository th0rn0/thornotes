package handler_test

import (
	"database/sql"
	"net/http"
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
