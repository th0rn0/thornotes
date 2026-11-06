package handler_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/model"
)

// One convention for "not yours" (ticket 475).
//
// An id the caller does not own must be indistinguishable from an id that does
// not exist: the same status and the same body. Anything else — a 403, a 200
// with an empty list, or a 404 whose body says "not permitted" — confirms that
// the id is real and turns the route into an enumeration oracle.
//
// The property is the *equality* of the two answers, so it is asserted as one
// table over every id-addressed route rather than as one test per endpoint. A
// per-endpoint test that only checks for 404 still passes while the body leaks
// "note not found" versus "forbidden", which is the same leak wearing a
// different hat.

const (
	// idNoRowCanHave is used for the "does not exist" half of every pair.
	idNoRowCanHave = int64(987654321)
	// notFoundBody is the single body a not-yours/not-there answer may carry:
	// writeError(apperror.ErrNotFound) and the router's NoRoute handler both
	// emit it, and the note routes already answer with it.
	notFoundBody = `{"error":"not found"}`
	// folderNotesRoute is the route ticket 475 was opened for.
	folderNotesRoute = "GET /api/v1/folders/:id/notes"
)

// idRoute is one id-addressed route. path receives the id as a string so the
// malformed-id table can reuse the same shape.
type idRoute struct {
	name   string
	method string
	path   func(id string) string
	body   any
	isNote bool // the :id addresses a note; otherwise a folder
	// ownerStatus is what the owner gets on their own resource, and is 0 when
	// the route is not registered (so no owner control is possible).
	ownerStatus int
}

// idRoutes is every route in the API that addresses a resource by id in the
// path. Folder routes are the ones ticket 475 is about; the note routes are
// included because they are the reference behaviour folders have to match
// (same status AND same body), and because the table is only worth anything
// if it covers the whole convention rather than the one endpoint that broke.
func idRoutes() []idRoute {
	return []idRoute{
		// ── Folders ──────────────────────────────────────────────────────────
		{
			name:   "GET /api/v1/folders/:id",
			method: http.MethodGet,
			path:   func(id string) string { return "/api/v1/folders/" + id },
			// No such route exists: gin's NoRoute answers JSON 404 for both
			// halves of the pair, which satisfies the equality but proves
			// nothing about handler logic. ownerStatus 0 records that.
			ownerStatus: 0,
		},
		{
			name:        "GET /api/v1/folders/:id/notes",
			method:      http.MethodGet,
			path:        func(id string) string { return "/api/v1/folders/" + id + "/notes" },
			ownerStatus: http.StatusOK,
		},
		{
			name:        "PATCH /api/v1/folders/:id",
			method:      http.MethodPatch,
			path:        func(id string) string { return "/api/v1/folders/" + id },
			body:        map[string]any{"name": "Renamed by the other user"},
			ownerStatus: http.StatusOK,
		},
		{
			name:        "PATCH /api/v1/folders/:id/move",
			method:      http.MethodPatch,
			path:        func(id string) string { return "/api/v1/folders/" + id + "/move" },
			body:        map[string]any{"parent_id": nil},
			ownerStatus: http.StatusOK,
		},
		{
			name:        "DELETE /api/v1/folders/:id",
			method:      http.MethodDelete,
			path:        func(id string) string { return "/api/v1/folders/" + id },
			ownerStatus: http.StatusNoContent,
		},

		// ── Notes (the behaviour folders must match) ─────────────────────────
		{
			name:        "GET /api/v1/notes/:id",
			method:      http.MethodGet,
			path:        func(id string) string { return "/api/v1/notes/" + id },
			isNote:      true,
			ownerStatus: http.StatusOK,
		},
		{
			name:        "PATCH /api/v1/notes/:id",
			method:      http.MethodPatch,
			path:        func(id string) string { return "/api/v1/notes/" + id },
			body:        map[string]any{"title": "Renamed by the other user"},
			isNote:      true,
			ownerStatus: http.StatusOK,
		},
		{
			name:        "PATCH /api/v1/notes/:id/move",
			method:      http.MethodPatch,
			path:        func(id string) string { return "/api/v1/notes/" + id + "/move" },
			body:        map[string]any{"folder_id": nil},
			isNote:      true,
			ownerStatus: http.StatusOK,
		},
		{
			name:        "POST /api/v1/notes/:id/share",
			method:      http.MethodPost,
			path:        func(id string) string { return "/api/v1/notes/" + id + "/share" },
			body:        map[string]any{},
			isNote:      true,
			ownerStatus: http.StatusOK,
		},
		{
			name:        "DELETE /api/v1/notes/:id",
			method:      http.MethodDelete,
			path:        func(id string) string { return "/api/v1/notes/" + id },
			isNote:      true,
			ownerStatus: http.StatusNoContent,
		},
	}
}

// callRoute performs one table request and returns its status and body.
func callRoute(t *testing.T, c *testClient, rt idRoute, id int64) (int, string) {
	t.Helper()
	return callRoutePath(t, c, rt, rt.path(strconv.FormatInt(id, 10)))
}

// callRoutePath performs the request against an explicit path, so the
// malformed-id case can go through the same call shape.
func callRoutePath(t *testing.T, c *testClient, rt idRoute, path string) (int, string) {
	t.Helper()
	resp := c.do(t, rt.method, path, rt.body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, strings.TrimSpace(string(raw))
}

// routeNamed fetches a table row by name so the follow-up assertions cannot
// silently start pointing at a different row.
func routeNamed(t *testing.T, name string) idRoute {
	t.Helper()
	for _, rt := range idRoutes() {
		if rt.name == name {
			return rt
		}
	}
	t.Fatalf("no route named %q in the table", name)
	return idRoute{}
}

// TestIDAddressedRoutes_NotYoursIsNotFound is the ticket-475 property: for
// every id-addressed route, asking as a user who owns nothing about the id
// gives exactly the same answer whether the id belongs to somebody else or to
// nobody at all.
func TestIDAddressedRoutes_NotYoursIsNotFound(t *testing.T) {
	// The fixture logs in as bob, with alice's folder/note/journal ids in it.
	f := newIsolationFixture(t)

	for _, rt := range idRoutes() {
		rt := rt
		t.Run(rt.name, func(t *testing.T) {
			ownedByAlice := f.folderID
			if rt.isNote {
				ownedByAlice = f.noteID
			}

			notYoursStatus, notYoursBody := callRoute(t, f.client, rt, ownedByAlice)
			missingStatus, missingBody := callRoute(t, f.client, rt, idNoRowCanHave)

			// The PR body's evidence table is this line, read back from a real
			// run rather than written down by hand.
			t.Logf("%s | as other user: %d %s | nonexistent id: %d %s",
				rt.name, notYoursStatus, notYoursBody, missingStatus, missingBody)

			// The convention itself: both halves are 404 …
			assert.Equal(t, http.StatusNotFound, notYoursStatus,
				"an id owned by another user must answer 404")
			assert.Equal(t, http.StatusNotFound, missingStatus,
				"an id that does not exist must answer 404")

			// … and the leak-free form of it: nothing distinguishes them.
			assert.Equal(t, missingStatus, notYoursStatus,
				"someone else's id and a nonexistent id must answer the same status")
			assert.Equal(t, missingBody, notYoursBody,
				"someone else's id and a nonexistent id must answer the same body")
			assert.Equal(t, notFoundBody, notYoursBody,
				"the not-yours body must be the API's single not-found body")
		})
	}

	// A blanket-404 regression (or a handler that 404s before it does its
	// work) would satisfy every assertion above, so prove the attempts changed
	// nothing: alice still has her folder, her note inside it, and its content.
	f.asAlice(t)

	status, body := callRoute(t, f.client, routeNamed(t, "GET /api/v1/folders/:id/notes"), f.folderID)
	require.Equal(t, http.StatusOK, status, "alice must still list her own folder")
	var items []*model.NoteListItem
	require.NoError(t, json.Unmarshal([]byte(body), &items))
	require.Len(t, items, 1, "alice's note must still be in her folder after bob's attempts")
	assert.Equal(t, "Alice private note", items[0].Title)

	status, body = callRoute(t, f.client, routeNamed(t, "GET /api/v1/notes/:id"), f.noteID)
	require.Equal(t, http.StatusOK, status, "alice must still read her own note")
	var note model.Note
	require.NoError(t, json.Unmarshal([]byte(body), &note))
	assert.Equal(t, "Alice private note", note.Title, "bob's rename must not have applied")
	assert.Contains(t, note.Content, isolationSecretTerm, "bob's write must not have applied")
}

// TestIDAddressedRoutes_OwnerUnaffected is the positive control: the same
// table, run by the owner on a resource of their own, must succeed. Without
// it, a middleware that rejected every id-addressed request would make the
// test above pass.
func TestIDAddressedRoutes_OwnerUnaffected(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	for i, rt := range idRoutes() {
		if rt.ownerStatus == 0 {
			t.Logf("%s: not a registered route, so there is no owner control "+
				"(the 404 comes from the router, not from a handler)", rt.name)
			continue
		}
		rt, i := rt, i
		t.Run(rt.name, func(t *testing.T) {
			// A fresh resource per row, so a mutating row cannot change the
			// outcome of the next one (and DELETE has something to delete).
			id := isolationCreateFolder(t, f.client, fmt.Sprintf("spare-%d", i))
			// Give the folder a note, so a "200 with an empty array" cannot
			// pass for a working list in the /notes row.
			noteInFolder := ""
			if !rt.isNote {
				noteInFolder = fmt.Sprintf("Spare note in folder %d", i)
				isolationCreateNote(t, f.client, &id, noteInFolder)
			}
			if rt.isNote {
				id = isolationCreateNote(t, f.client, nil, fmt.Sprintf("Spare note %d", i))
			}

			status, body := callRoute(t, f.client, rt, id)
			t.Logf("%s | as owner: %d %s", rt.name, status, body)
			assert.Equal(t, rt.ownerStatus, status,
				"the owner acting on their own resource must get %d, body %q",
				rt.ownerStatus, body)
			assert.NotEqual(t, notFoundBody, body,
				"the owner must not receive the not-found body")
			if noteInFolder != "" && rt.name == folderNotesRoute {
				assert.Contains(t, body, noteInFolder,
					"the owner's own folder must still list the notes inside it")
			}
		})
	}
}

// TestFolderRoutes_MalformedIDIs400 pins the other carve-out: a non-numeric id
// is a client bug, not a permission question, so the handler answers 400 and
// never 404. A route that never reaches the handler (GET /api/v1/folders/:id,
// which does not exist) cannot answer either code, so it is not in this table.
func TestFolderRoutes_MalformedIDIs400(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t) // identical for any session: this is parsing, not authorization

	for _, rt := range idRoutes() {
		if rt.isNote || rt.ownerStatus == 0 {
			continue
		}
		rt := rt
		t.Run(rt.name, func(t *testing.T) {
			status, body := callRoutePath(t, f.client, rt, rt.path("abc"))
			assert.Equal(t, http.StatusBadRequest, status,
				"a malformed id is a client bug: 400, body %q", body)
		})
	}
}
