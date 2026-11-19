package handler_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/model"
)

// Folder shares, slice 4 (ticket 616): the exposure pack.
//
// Three assertions, one file, and all three are about what a share CANNOT do:
//
//  1. a note moved into the shared subtree is published at once, and a note
//     moved out of it is gone at once — the view is live, not a snapshot, in
//     both directions;
//  2. a folder link is LISTABLE: GET /api/v1/shares carries it, with kind
//     "folder", its folder_id and a null note_id. This is the row slice 3
//     added, and the test that would have caught slice 3's absence;
//  3. the two public surfaces refuse a dead link the SAME way. The note route
//     /s/<token> and the folder route /f/<token> answer one serialised
//     response for every dead state — expired, revoked, unknown — compared
//     whole, because an equal status code is not the assertion.
//
// The file shares its fixtures with the suites next door (newFolderShareFixture,
// expireFolderShare, shareByKind) rather than building a harness of its own:
// a second fixture tree would be a second thing that can be wrong.

// shareListRows fetches GET /api/v1/shares and decodes it as raw JSON objects.
//
// The typed model is not enough for the shape assertion: a field that was
// renamed, added or dropped on the wire decodes into the same struct either
// way, so comparing the raw row is what makes a shape change fail a test.
func shareListRows(t *testing.T, c *testClient) []map[string]any {
	t.Helper()
	resp := c.do(t, http.MethodGet, "/api/v1/shares", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var rows []map[string]any
	decodeInto(t, resp, &rows)
	return rows
}

// captureBody reads a response body once and puts it back, so the SAME
// response can be inspected (status, headers, body) and then serialised whole
// for a cross-surface comparison. Without the put-back the serialised form
// would carry an empty body and the comparison would prove nothing.
func captureBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	return raw
}

// ─── 1. moved in, moved out ──────────────────────────────────────────────────

// TestFolderShare_MovedNote_LiveInBothDirections is the "live, not a snapshot"
// promise in both directions.
//
// A folder share is a window on the folder as it is NOW. A note moved into
// the shared subtree is published the moment the move lands — not after a
// reconcile pass, and not after a background job noticed the disk moved — and
// a note moved out of it stops answering at once. A share nobody proved cannot
// leak is not a share.
//
// The move is made through the API, which writes the row synchronously, and
// the assertion runs on the very next request: there is no sleep, no poll and
// no reconcile anywhere in this test. That is the point of it — a reconciler
// running in the background would make a snapshot implementation look live.
func TestFolderShare_MovedNote_LiveInBothDirections(t *testing.T) {
	f := newFolderShareFixture(t)

	// A note outside the shared subtree: at the vault root, above the shared
	// folder. It must not be reachable through the link yet.
	outside := isolationCreateNote(t, f.client, nil, "Moved Note")
	const slug = "moved-note"

	status, _ := anonBody(t, f.client, "/f/"+f.token+"/n/"+slug)
	require.Equal(t, http.StatusNotFound, status,
		"a note outside the shared subtree must not be readable through the link")
	listing := readAnonymousPage(t, f.client, "/f/"+f.token)
	require.NotContains(t, listing, "Moved Note",
		"the fixture is wrong: the note outside the subtree must not be listed")

	// ── moved in ─────────────────────────────────────────────────────────────
	//
	// The move is an ordinary owner action on the note: it does not touch the
	// share row, the token or the folder. What changes is which folder the
	// note belongs to, and the shared view reads that per request.
	resp := f.client.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(outside)+"/move",
		map[string]any{"folder_id": f.rootID})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"moving a note into the shared folder must succeed")

	// The row itself: the move is committed, not pending. If this were
	// waiting for a reconcile, the folder the row points at would still be
	// the old one here.
	var folderID *int64
	require.NoError(t, f.client.pool.ReadDB.QueryRow(
		`SELECT folder_id FROM notes WHERE id = ?`, outside).Scan(&folderID))
	require.NotNil(t, folderID, "the moved note must live in a folder")
	require.Equal(t, f.rootID, *folderID,
		"the row must point at the shared folder immediately — no reconcile pass in between")

	// And the link serves it on the very next request.
	listing = readAnonymousPage(t, f.client, "/f/"+f.token)
	require.Contains(t, listing, "Moved Note",
		"a note moved INTO the shared subtree must be listed on the link at once — "+
			"the view is live, not a snapshot")
	require.Contains(t, listing, `href="/f/`+f.token+`/n/`+slug+`"`,
		"the moved-in note must be addressable under the share token")

	page := readAnonymousPage(t, f.client, "/f/"+f.token+"/n/"+slug)
	assert.Contains(t, page, "Moved Note",
		"the moved-in note must be READABLE through the link, not merely listed")

	// ── moved out ────────────────────────────────────────────────────────────
	//
	// The same note, moved back out of the shared subtree (into the folder
	// above it). The share row is untouched — the token still exists and still
	// resolves the folder — so nothing about the LINK has changed. Only the
	// note's place in the tree has, and the link must notice at once.
	resp = f.client.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(outside)+"/move",
		map[string]any{"folder_id": f.outerID})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"moving a note out of the shared subtree must succeed")

	listing = readAnonymousPage(t, f.client, "/f/"+f.token)
	assert.NotContains(t, listing, "Moved Note",
		"a note moved OUT of the shared subtree must stop being listed at once")

	// The read of the note is the uniform refusal: the same answer as a
	// token that never existed, compared whole.
	const neverIssued = "0123456789abcdef0123456789abcdef"
	movedOut := serialiseResponse(t, anonGet(t, f.client, "/f/"+f.token+"/n/"+slug))
	unknown := serialiseResponse(t, anonGet(t, f.client, "/f/"+neverIssued+"/n/"+slug))
	assert.Equal(t, unknown, movedOut,
		"a note moved out of the shared subtree must answer exactly what an unknown token "+
			"answers — compared as whole responses, because an equal status is not the assertion")
}

// ─── 2. the listable folder link ─────────────────────────────────────────────

// TestFolderShare_FolderLink_ListableAsAWholeRow is the regression for the
// first unlistable capability in the product: a folder link that cannot be
// found cannot be revoked, and a folder link is recursive, so not finding one
// leaves a whole subtree exposed with no way to close it.
//
// Before slice 3, GET /api/v1/shares answered note links only, so a folder
// link was invisible to the one endpoint whose whole job is "what have I
// handed out".
//
// The row is asserted AS A WHOLE — one comparison over the whole JSON object,
// not a field at a time. A field-at-a-time assertion survives a shape change
// (a renamed field, an added field, a dropped field all leave the fields it
// does look at untouched), and the shape is the contract: a client reads this
// endpoint to find a link to revoke.
func TestFolderShare_FolderLink_ListableAsAWholeRow(t *testing.T) {
	f := newFolderShareFixture(t)

	// The typed view, through the helper the slice-3 suite already uses.
	shares := listShares(t, f.client)
	require.Len(t, shares, 1, "the folder link must be listed at all — before slice 3 it was not")
	folder := shareByKind(t, shares, model.ShareKindFolder)
	require.NotNil(t, folder.FolderID, "a folder row carries the folder the revoke route needs")
	assert.Equal(t, f.rootID, *folder.FolderID)
	assert.Nil(t, folder.NoteID, "a folder link is not a note link: its note_id is null")

	// The raw view, as the wire sent it.
	rows := shareListRows(t, f.client)
	require.Len(t, rows, 1, "exactly one row: the folder link")

	// updated_at is the share row's created_at — a real instant, but not one
	// the test can predict, so it is checked for shape and the rest of the row
	// is compared as one object.
	stamp, ok := rows[0]["updated_at"].(string)
	require.True(t, ok, "updated_at must be present and a string, got %v", rows[0]["updated_at"])
	_, err := time.Parse(time.RFC3339, stamp)
	require.NoError(t, err, "updated_at must be an RFC3339 instant: %q", stamp)

	row := rows[0]
	row["updated_at"] = "<an instant>"

	assert.Equal(t, map[string]any{
		"kind":           "folder",
		"note_id":        nil,
		"folder_id":      float64(f.rootID),
		"title":          "work",
		"token_prefix":   f.token[:8] + "…",
		"expires_at":     nil,
		"last_viewed_at": nil,
		"updated_at":     "<an instant>",
	}, row,
		"the folder row must be present with this exact shape — a renamed, added or "+
			"dropped field changes this comparison")
}

// ─── 3. the refusal is the same on both surfaces ─────────────────────────────

// TestShareRefusal_FolderAndNoteSurfaces_AnswerIdentically is the cross-surface
// consistency of the refusal.
//
// /s/<token> and /f/<token> are two handlers, two services and two tables, and
// they are the same kind of capability: a token in a URL, read with no
// session. The three dead states — expired, revoked, unknown — must produce
// ONE answer between them, compared as whole serialised responses, not as
// status codes.
//
// Two branches that both say 404 can differ in a header or a body, and any
// difference is an oracle: it confirms that a token was real once, and when
// it died, which is information the holder of a burned URL must not be able to
// get. A later divergence between the two surfaces should fail a test rather
// than wait for a review.
func TestShareRefusal_FolderAndNoteSurfaces_AnswerIdentically(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	const neverIssued = "0123456789abcdef0123456789abcdef"

	// ── the three dead states, on both surfaces ──────────────────────────────

	// Expired: a deadline set through each surface's own API, then passed by
	// moving the clock in the database (the API refuses a past deadline, so
	// this is the only way to travel through time without sleeping).
	expiredNote := isolationCreateNote(t, f.client, nil, "Expiring note")
	expiredNoteToken := shareNote(t, f.client, expiredNote, map[string]any{
		"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})
	expireShare(t, f.client.pool, expiredNote)

	expiredFolder := isolationCreateFolder(t, f.client, "Expiring folder")
	expiredFolderToken := shareFolder(t, f.client, expiredFolder, map[string]any{
		"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})
	expireFolderShare(t, f.client.pool, expiredFolder)

	// Revoked: each surface's own revoke route, which deletes the link.
	revokedNote := isolationCreateNote(t, f.client, nil, "Revoked note")
	revokedNoteToken := shareNote(t, f.client, revokedNote, nil)
	resp := f.client.do(t, http.MethodPost, "/api/v1/notes/"+i64str(revokedNote)+"/share",
		map[string]any{"clear": true})
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "revoking a note link must succeed")

	revokedFolder := isolationCreateFolder(t, f.client, "Revoked folder")
	revokedFolderToken := shareFolder(t, f.client, revokedFolder, nil)
	revokeFolderShare(t, f.client, revokedFolder)

	// Live controls: neither surface may be answering 404 to everything, or
	// the comparisons below would prove nothing.
	liveNoteToken := shareNote(t, f.client, f.noteID, nil)
	liveFolderToken := shareFolder(t, f.client, f.folderID, nil)

	states := []struct {
		name       string
		notePath   string
		folderPath string
	}{
		{"expired", "/s/" + expiredNoteToken, "/f/" + expiredFolderToken},
		{"revoked", "/s/" + revokedNoteToken, "/f/" + revokedFolderToken},
		{"unknown", "/s/" + neverIssued, "/f/" + neverIssued},
	}

	for _, state := range states {
		state := state
		t.Run(state.name, func(t *testing.T) {
			noteResp := anonGet(t, f.client, state.notePath)
			noteBody := captureBody(t, noteResp)

			folderResp := anonGet(t, f.client, state.folderPath)
			folderBody := captureBody(t, folderResp)

			// The whole response, not the status code: the two surfaces must
			// be byte-identical once the clock-dependent Date header is set
			// aside.
			noteSerialised := serialiseResponse(t, noteResp)
			folderSerialised := serialiseResponse(t, folderResp)
			assert.Equal(t, noteSerialised, folderSerialised,
				"the folder route and the note route must answer one refusal, not two")

			// And the shape of that refusal, stated explicitly so a change
			// to it fails here rather than in a review.
			for _, r := range []struct {
				surface string
				resp    *http.Response
				body    []byte
			}{
				{"note", noteResp, noteBody},
				{"folder", folderResp, folderBody},
			} {
				assert.Equal(t, http.StatusNotFound, r.resp.StatusCode,
					"the %s surface must answer 404 for a dead link", r.surface)
				assert.Empty(t, r.body,
					"the %s surface must answer with an empty body: %q", r.surface, r.body)
				assert.Equal(t, "no-store", r.resp.Header.Get("Cache-Control"),
					"a dead link is still a capability: nothing may cache its answer (%s)", r.surface)
				assert.Equal(t, "noindex", r.resp.Header.Get("X-Robots-Tag"),
					"a pasted link must not enter a search index (%s)", r.surface)
			}
		})
	}

	// Positive control: the live links are NOT in the dead-link equivalence
	// class, so the comparisons above cannot pass by both surfaces refusing
	// everything.
	liveNote := serialiseResponse(t, anonGet(t, f.client, "/s/"+liveNoteToken))
	liveFolder := serialiseResponse(t, anonGet(t, f.client, "/f/"+liveFolderToken))
	deadNote := serialiseResponse(t, anonGet(t, f.client, "/s/"+neverIssued))
	deadFolder := serialiseResponse(t, anonGet(t, f.client, "/f/"+neverIssued))

	assert.NotEqual(t, deadNote, liveNote, "the live note link must answer differently from a dead one")
	assert.NotEqual(t, deadFolder, liveFolder, "the live folder link must answer differently from a dead one")
}
