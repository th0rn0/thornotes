package handler_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/db"
)

// Folder shares, slice 1 (ticket 616): POST /api/v1/folders/:id/share and the
// anonymous HTML surface at /f/<token>.
//
// The shape of these tests follows the note-share suite next door
// (share_lifecycle_test.go), because the two surfaces are the same kind of
// capability and must answer the same way. Three properties are the point of
// the ticket and none of them is a status code:
//
//   - an anonymous failure — unknown token, revoked link, expired link, crafted
//     path — is ONE answer, compared as the whole serialised response;
//   - the view is live, so a note added after the share exists is published
//     (asserted in a test whose name says so, so nobody "fixes" it later);
//   - the token never reaches the access log.

// ─── helpers ─────────────────────────────────────────────────────────────────

// shareFolder issues (or rotates) a folder link and returns its token.
func shareFolder(t *testing.T, c *testClient, folderID int64, body map[string]any) string {
	t.Helper()
	resp := c.do(t, http.MethodPost, "/api/v1/folders/"+i64str(folderID)+"/share", body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out map[string]any
	decodeInto(t, resp, &out)
	token, ok := out["share_token"].(string)
	require.True(t, ok, "sharing must return a token, got %v", out["share_token"])
	require.NotEmpty(t, token)
	return token
}

// revokeFolderShare revokes a folder link through the API and asserts the
// payload the UI relies on: a null token.
func revokeFolderShare(t *testing.T, c *testClient, folderID int64) {
	t.Helper()
	resp := c.do(t, http.MethodPost, "/api/v1/folders/"+i64str(folderID)+"/share",
		map[string]any{"clear": true})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out map[string]any
	decodeInto(t, resp, &out)
	assert.Nil(t, out["share_token"], "revoking must report no token, not a fresh one")
}

// anonGet performs a request with no session cookie and no CSRF token, which is
// the only honest way to test an anonymous route: the test client carries
// alice's session, and a handler that (wrongly) accepted it would pass a test
// that used it.
func anonGet(t *testing.T, c *testClient, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, c.server.URL+path, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// anonBody performs an anonymous GET and returns its status and body.
func anonBody(t *testing.T, c *testClient, path string) (int, string) {
	t.Helper()
	resp := anonGet(t, c, path)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

// readAnonymousPage performs an anonymous GET that must succeed, and returns the
// rendered HTML.
func readAnonymousPage(t *testing.T, c *testClient, path string) string {
	t.Helper()
	resp := anonGet(t, c, path)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "expected %s to serve the page", path)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(raw)
}

// createSubfolder creates a root folder and immediately moves it under parent,
// because POST /api/v1/folders only ever creates a root.
func createSubfolder(t *testing.T, c *testClient, name string, parent int64) int64 {
	t.Helper()
	id := isolationCreateFolder(t, c, name)
	resp := c.do(t, http.MethodPatch, "/api/v1/folders/"+i64str(id)+"/move",
		map[string]any{"parent_id": parent})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	return id
}

// folderShareFixture is alice's three-level tree with a share on the middle
// level:
//
//	topsecret/            (never shared, and its name must never leak)
//	    work/             (the shared root)
//	        Work note     (one level down)
//	        projects/     (a subfolder, i.e. a link down)
//	            Plan note (three levels down)
type folderShareFixture struct {
	client   *testClient
	outerID  int64
	rootID   int64
	middleID int64
	token    string
}

func newFolderShareFixture(t *testing.T) *folderShareFixture {
	t.Helper()
	c := newTestClient(t)
	isolationLogin(t, c, "alice")

	outerID := isolationCreateFolder(t, c, "topsecret")
	rootID := createSubfolder(t, c, "work", outerID)
	middleID := createSubfolder(t, c, "projects", rootID)

	isolationCreateNote(t, c, &rootID, "Work note")
	isolationCreateNote(t, c, &middleID, "Plan note")

	f := &folderShareFixture{
		client:   c,
		outerID:  outerID,
		rootID:   rootID,
		middleID: middleID,
	}
	f.token = shareFolder(t, c, rootID, nil)
	return f
}

// ─── the headline behaviours ─────────────────────────────────────────────────

// TestFolderShare_LiveView_NewNoteAppears is the test that keeps "live" from
// being "fixed" into a snapshot.
//
// A folder share is a window on the folder as it is now; adding a note to a
// shared folder publishes it. That is a product decision (contract decision 1),
// and the cost of a snapshot is a second copy of the vault that breaks "the
// directory on disk is canonical". If somebody later replaces the live query
// with a captured list, this test must be deleted deliberately, not silently
// broken.
func TestFolderShare_LiveView_NewNoteAppears(t *testing.T) {
	f := newFolderShareFixture(t)
	f.client.login(t, "alice", isolationAlicePassword)

	body := readAnonymousPage(t, f.client, "/f/"+f.token)
	require.Contains(t, body, "Work note", "the shared folder must list the notes it already has")
	assert.NotContains(t, body, "Added after the link was created",
		"the fixture is wrong: the later note must not exist yet")

	isolationCreateNote(t, f.client, &f.rootID, "Added after the link was created")

	body = readAnonymousPage(t, f.client, "/f/"+f.token)
	assert.Contains(t, body, "Added after the link was created",
		"a note added to a shared folder AFTER the share was created must appear on the link — "+
			"the view is live, not a snapshot")
}

// TestFolderShare_Revoke_StopsResolvingImmediately takes the URL and watches it
// stop resolving, and the SECOND read of the same URL is the assertion: a
// revoke that leaves the token resolving is worse than no revoke button (#49).
func TestFolderShare_Revoke_StopsResolvingImmediately(t *testing.T) {
	f := newFolderShareFixture(t)
	f.client.login(t, "alice", isolationAlicePassword)

	listingURL := "/f/" + f.token
	noteURL := "/f/" + f.token + "/n/projects/plan-note"

	status, _ := anonBody(t, f.client, listingURL)
	require.Equal(t, http.StatusOK, status, "the link must work before it is revoked")
	status, _ = anonBody(t, f.client, noteURL)
	require.Equal(t, http.StatusOK, status)

	revokeFolderShare(t, f.client, f.rootID)

	for _, path := range []string{listingURL, noteURL} {
		status, _ := anonBody(t, f.client, path)
		assert.Equal(t, http.StatusNotFound, status,
			"a revoked link must stop resolving on the very next request: %s", path)
	}
}

// TestFolderShare_AllFailures_IdenticalResponse is the 469/475 precedent applied
// to the folder surface: an unknown token, a revoked link, an expired link and a
// crafted path must all be ONE answer when the response is serialised whole.
//
// Status-code equality is not the assertion. Two branches that both say 404 can
// differ in a header, and any difference confirms the token was real once — or
// tells the holder of a burned URL when it died.
func TestFolderShare_AllFailures_IdenticalResponse(t *testing.T) {
	f := newFolderShareFixture(t)
	f.client.login(t, "alice", isolationAlicePassword)

	const neverIssued = "0123456789abcdef0123456789abcdef"

	// The expired half: slice 1 has no writer for expires_at (expiry is slice 2),
	// so the clock is moved in the database — which is also the assertion that
	// the expiry RULE is the shared one (notes.ShareLinkExpired) rather than a
	// second implementation that might disagree.
	expiringFolder := isolationCreateFolder(t, f.client, "expiring")
	expiredToken := shareFolder(t, f.client, expiringFolder, nil)
	_, err := f.client.pool.WriteDB.Exec(
		`UPDATE folder_shares SET expires_at = ? WHERE folder_id = ?`,
		time.Now().UTC().Add(-time.Hour), expiringFolder)
	require.NoError(t, err)

	revokedFolder := isolationCreateFolder(t, f.client, "revoked-by-api")
	revokedToken := shareFolder(t, f.client, revokedFolder, nil)
	revokeFolderShare(t, f.client, revokedFolder)

	// A crafted path on a LIVE token: it must be indistinguishable from a token
	// that never existed, or the guard itself becomes the oracle.
	crafted := []string{
		"/f/" + f.token + "/n/%2e%2e/secret",
		"/f/" + f.token + "/d/%2e%2e",
		"/f/" + f.token + "/n/projects/../../../etc/passwd",
		"/f/" + f.token + "/n/no-such-note",
		"/f/" + f.token + "/n/1",
	}

	routes := []string{
		"/f/%s",
		"/f/%s/n/projects/plan-note",
		"/f/%s/d/projects",
	}

	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			reference := serialiseResponse(t, anonGet(t, f.client, fmt.Sprintf(route, neverIssued)))

			for _, token := range []string{expiredToken, revokedToken} {
				got := serialiseResponse(t, anonGet(t, f.client, fmt.Sprintf(route, token)))
				assert.Equal(t, reference, got,
					"an expired/revoked link on %s must be indistinguishable from a token that never existed — "+
						"compared as whole responses, because an equal status is not the assertion", route)
			}

			// The live token on the same route is NOT in the equivalence class, so
			// the comparison above cannot pass by the handler 404ing everything.
			live := serialiseResponse(t, anonGet(t, f.client, fmt.Sprintf(route, f.token)))
			assert.NotEqual(t, reference, live,
				"the live link must answer differently from a dead one — otherwise this test proves nothing")
		})
	}

	for _, path := range crafted {
		path := path
		t.Run(path, func(t *testing.T) {
			got := serialiseResponse(t, anonGet(t, f.client, path))
			want := serialiseResponse(t, anonGet(t, f.client, "/f/"+neverIssued))
			// A crafted path and an unknown token both resolve to "no such page":
			// same status, same headers, same empty body.
			assert.Equal(t, want, got,
				"a crafted path must answer exactly what an unknown token answers")
		})
	}
}

// TestFolderShare_NoStoreOnEveryOutcome pins the two headers on the 200 AND on
// the 404 of all three routes. Headers are set before the first branch in the
// handler; this is the test that notices when a later refactor moves them.
func TestFolderShare_NoStoreOnEveryOutcome(t *testing.T) {
	f := newFolderShareFixture(t)
	const neverIssued = "ffffffffffffffffffffffffffffffff"

	assertHeaders := func(t *testing.T, path string, wantStatus int) {
		t.Helper()
		resp := anonGet(t, f.client, path)
		defer resp.Body.Close()
		require.Equal(t, wantStatus, resp.StatusCode, "status for %s", path)
		assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"),
			"a share URL is a capability: nothing between here and the reader may cache its answer (%s)", path)
		assert.Equal(t, "noindex", resp.Header.Get("X-Robots-Tag"),
			"a pasted link must not enter a search index (%s)", path)
	}

	for _, path := range []string{
		"/f/" + f.token,
		"/f/" + f.token + "/n/projects/plan-note",
		"/f/" + f.token + "/d/projects",
	} {
		assertHeaders(t, path, http.StatusOK)
	}

	for _, path := range []string{
		"/f/" + neverIssued,
		"/f/" + neverIssued + "/n/whatever",
		"/f/" + neverIssued + "/d/whatever",
		"/f/" + f.token + "/n/%2e%2e/x",
	} {
		assertHeaders(t, path, http.StatusNotFound)
	}
}

// TestFolderShare_AnonymousResponse_HasNoIdsAndNoAncestorNames is the exposure
// review as an assertion: the anonymous HTML carries no database id, no link
// into the authenticated app, and no folder name above the shared root.
//
// The ancestor is called "topsecret" so the check cannot pass by coincidence,
// and every href is inspected rather than a sample of them.
func TestFolderShare_AnonymousResponse_HasNoIdsAndNoAncestorNames(t *testing.T) {
	f := newFolderShareFixture(t)
	f.client.login(t, "alice", isolationAlicePassword)

	pages := map[string]string{
		"root listing":           "/f/" + f.token,
		"subfolder":              "/f/" + f.token + "/d/projects",
		"note three levels down": "/f/" + f.token + "/n/projects/plan-note",
	}

	for name, path := range pages {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			body := readAnonymousPage(t, f.client, path)

			assert.NotContains(t, body, "topsecret",
				"a folder name above the shared root must never appear in an anonymous response")
			assert.NotContains(t, body, "/api/",
				"an anonymous reader must not be handed a URL into the authenticated API")
			assert.NotContains(t, body, "/notes/",
				"an anonymous reader must not be handed an id-addressed app route")
			assert.NotContains(t, body, "data-id",
				"no element may carry a database id in a data attribute")
			assert.NotRegexp(t, regexp.MustCompile(`id="\d+"`), body,
				"no element may carry a database id in an id attribute")

			// Every link the page emits is either a public asset or under the same
			// token, and none of them names a note or folder by id. (/static/ is
			// the shared note template's highlight.js stylesheet — public bytes,
			// not a route into anything.)
			href := regexp.MustCompile(`href="([^"]*)"`)
			matches := href.FindAllStringSubmatch(body, -1)
			require.NotEmpty(t, matches, "the listing must link somewhere, or this test proves nothing")
			for _, m := range matches {
				url := m[1]
				if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "/static/") {
					continue
				}
				assert.True(t, strings.HasPrefix(url, "/f/"+f.token),
					"every link must stay under this share token, got %q", url)
				assert.NotContains(t, url, "id=")
			}

			// The breadcrumb starts at the shared root's own name — the first thing
			// a reader may know about the tree.
			if name != "note three levels down" {
				assert.NotContains(t, body, "topsecret/work",
					"a path above the root must not be renderable at all")
			}
		})
	}

	// The root listing shows the shared root's own name, so the assertions above
	// cannot be passing because the page rendered nothing.
	root := readAnonymousPage(t, f.client, "/f/"+f.token)
	assert.Contains(t, root, "work", "the shared root's own name is the breadcrumb's first element")
	assert.Contains(t, root, "Work note")
}

// TestFolderShare_SubfolderIsALinkDown_AndDeepNoteIsVisibleWhenReached is the
// recursion acceptance: a subfolder is a navigable entry (not an inlined tree),
// and a note three levels down is reachable by walking down.
func TestFolderShare_SubfolderIsALinkDown_AndDeepNoteIsVisibleWhenReached(t *testing.T) {
	f := newFolderShareFixture(t)

	root := readAnonymousPage(t, f.client, "/f/"+f.token)
	require.Contains(t, root, `href="/f/`+f.token+`/d/projects"`,
		"a subfolder must be a link down")
	assert.NotContains(t, root, "Plan note",
		"a note three levels down must not be inlined into the root listing — one level at a time")

	sub := readAnonymousPage(t, f.client, "/f/"+f.token+"/d/projects")
	assert.Contains(t, sub, "Plan note", "walking down must show the notes one level down")
	assert.Contains(t, sub, `href="/f/`+f.token+`/n/projects/plan-note"`,
		"the note must be addressable from the level that lists it")

	note := readAnonymousPage(t, f.client, "/f/"+f.token+"/n/projects/plan-note")
	assert.Contains(t, note, "Plan note", "the note read view must serve the note")
}

// TestFolderShare_LevelCappedAt200_WithWorkingCursor proves the ceiling and the
// cursor with 201 entries in each section: the first page carries 200, the second
// carries the single remainder, and the second page does not repeat the 200th.
func TestFolderShare_LevelCappedAt200_WithWorkingCursor(t *testing.T) {
	f := newFolderShareFixture(t)
	capFolder := isolationCreateFolder(t, f.client, "capped")
	token := shareFolder(t, f.client, capFolder, nil)

	// Inserted directly: 402 HTTP writes would test the note-create route, not
	// the listing, and would dominate the suite's runtime.
	require.NoError(t, seedCappedNotes(t, f.client.pool, capFolder, 201))
	require.NoError(t, seedCappedFolders(t, f.client.pool, capFolder, 201))

	t.Run("notes", func(t *testing.T) {
		first := readAnonymousPage(t, f.client, "/f/"+token)
		assert.Equal(t, 200, strings.Count(first, `href="/f/`+token+`/n/`),
			"one section is capped at 200 entries per request")
		require.Contains(t, first, "?na=cap-200",
			"the first page must offer the cursor for the next one")

		second := readAnonymousPage(t, f.client, "/f/"+token+"?na=cap-200")
		assert.Equal(t, 1, strings.Count(second, `href="/f/`+token+`/n/`),
			"the second page must carry exactly the remainder")
		assert.Contains(t, second, "cap-201")
		assert.NotContains(t, second, `href="/f/`+token+`/n/cap-200"`,
			"the second page must not repeat the 200th entry that closed the first")
	})

	t.Run("folders", func(t *testing.T) {
		first := readAnonymousPage(t, f.client, "/f/"+token)
		assert.Equal(t, 200, strings.Count(first, `href="/f/`+token+`/d/`),
			"the folders section is capped independently of the notes section")
		require.Contains(t, first, "?fa=f-200",
			"the first page must offer the folders cursor")

		second := readAnonymousPage(t, f.client, "/f/"+token+"?fa=f-200")
		assert.Equal(t, 1, strings.Count(second, `href="/f/`+token+`/d/`),
			"the second page must carry exactly the remainder")
		assert.Contains(t, second, "f-201")
		assert.NotContains(t, second, `href="/f/`+token+`/d/f-200"`,
			"the second page must not repeat the 200th folder")
	})
}

// TestFolderShare_MoveAndRename_DoesNotRevoke pins a property that looks like a
// bug when read later: the share is keyed on folder_id, so renaming or moving the
// folder keeps the token working (a note share survives a note move the same way).
func TestFolderShare_MoveAndRename_DoesNotRevoke(t *testing.T) {
	f := newFolderShareFixture(t)
	f.client.login(t, "alice", isolationAlicePassword)

	resp := f.client.do(t, http.MethodPatch, "/api/v1/folders/"+i64str(f.rootID),
		map[string]any{"name": "renamed"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	body := readAnonymousPage(t, f.client, "/f/"+f.token)
	assert.Contains(t, body, "renamed", "a rename must be visible on the link")

	// Move the shared root out from under its own parent, to the top level.
	resp = f.client.do(t, http.MethodPatch, "/api/v1/folders/"+i64str(f.rootID)+"/move",
		map[string]any{"parent_id": nil})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	body = readAnonymousPage(t, f.client, "/f/"+f.token)
	assert.Contains(t, body, "renamed", "a move must not revoke the link")
	assert.NotContains(t, body, "topsecret")
}

// ─── the write side ──────────────────────────────────────────────────────────

// TestFolderShare_ShareOtherUsersFolder_404AndNothingPersisted is the #40 shape,
// repeated for folders: bob's attempt answers the API's single not-found body,
// alice's folder still has no share row afterwards, and her own attempt still
// works (the positive control).
func TestFolderShare_ShareOtherUsersFolder_404AndNothingPersisted(t *testing.T) {
	f := newIsolationFixture(t) // the session is bob's

	resp := f.client.do(t, http.MethodPost, "/api/v1/folders/"+i64str(f.folderID)+"/share",
		map[string]any{})
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode,
		"bob must not be told he shared alice's folder")
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, notFoundBody, strings.TrimSpace(string(raw)),
		"somebody else's folder and a nonexistent folder must be the same body")

	var count int
	require.NoError(t, f.client.pool.ReadDB.QueryRow(
		`SELECT COUNT(*) FROM folder_shares WHERE folder_id = ?`, f.folderID).Scan(&count))
	assert.Zero(t, count, "no share row may exist after bob's attempt")

	// The unknown-id half answers the same body, or the route is an oracle.
	resp = f.client.do(t, http.MethodPost, "/api/v1/folders/"+i64str(idNoRowCanHave)+"/share",
		map[string]any{})
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	raw, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, notFoundBody, strings.TrimSpace(string(raw)))

	// Positive control: alice can share the same folder.
	f.asAlice(t)
	token := shareFolder(t, f.client, f.folderID, nil)
	status, body := anonBody(t, f.client, "/f/"+token)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, "Alice private note")
}

// TestFolderShare_MalformedBody_400AndTouchesNothing is the body rule the note
// endpoints got wrong once: a body that does not decode must never fall through
// to "issue a token". Only a body that is absent entirely is tolerated.
func TestFolderShare_MalformedBody_400AndTouchesNothing(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)
	path := "/api/v1/folders/" + i64str(f.folderID) + "/share"

	rows := func() int {
		t.Helper()
		var n int
		require.NoError(t, f.client.pool.ReadDB.QueryRow(
			`SELECT COUNT(*) FROM folder_shares WHERE folder_id = ?`, f.folderID).Scan(&n))
		return n
	}

	// The body that once created a working public link: a string where a bool is
	// expected.
	resp := f.client.do(t, http.MethodPost, path, map[string]any{"clear": "true"})
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"an undecodable body must be refused, not treated as 'share'")
	assert.Contains(t, string(raw), "request body must be a JSON object")
	assert.Zero(t, rows(), "a refused request must not leave a token behind")

	// An unknown field is still refused: `expires` is a typo for `expires_at`,
	// and a caller who believed they set a deadline must be told rather than
	// handed an immortal link they think is dated.
	resp = f.client.do(t, http.MethodPost, path,
		map[string]any{"expires": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"an unknown field must still be refused, not silently ignored")
	assert.Zero(t, rows())

	// expires_at IS accepted (slice 2): it is the same field the note-share route
	// takes, and it applies to the link this request creates.
	resp = f.client.do(t, http.MethodPost, path,
		map[string]any{"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	require.Equal(t, 1, rows(), "a share carrying a deadline must store exactly one link")
	assert.NotNil(t, storedFolderExpiry(t, f.client.pool, f.folderID),
		"slice 2: expires_at must be stored on the row, not ignored")

	// Revoking clears the row (and with it the deadline), which is where the
	// fixture for the remaining assertions starts.
	revokeFolderShare(t, f.client, f.folderID)
	assert.Zero(t, rows(), "revoking deletes the row")

	// A body that is absent entirely means "share", which is what the UI's
	// one-click button sends.
	token := shareFolder(t, f.client, f.folderID, nil)
	assert.Len(t, token, 32, "a share token is 32 hex characters")

	// Re-sharing rotates: POST without clear always issues a new token, exactly
	// as the note route does.
	second := shareFolder(t, f.client, f.folderID, map[string]any{})
	assert.NotEqual(t, token, second, "re-sharing must rotate the link, mirroring notes")
	status, _ := anonBody(t, f.client, "/f/"+token)
	assert.Equal(t, http.StatusNotFound, status, "the rotated link must stop resolving")
	status, _ = anonBody(t, f.client, "/f/"+second)
	assert.Equal(t, http.StatusOK, status)

	// Revoking a folder that has no link is success, not not-found: the caller
	// has already established the folder is theirs.
	revokeFolderShare(t, f.client, f.folderID)
	assert.Zero(t, rows(), "revoking deletes the row")
	revokeFolderShare(t, f.client, f.folderID)
}

// TestFolderShare_TokenNeverReachesTheAccessLog drives the real route with the
// real middleware and reads the log line back: a working capability in a log
// file is a token that outlives its link, in a place nobody thinks to revoke.
func TestFolderShare_TokenNeverReachesTheAccessLog(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = previous })

	f := newFolderShareFixture(t)

	paths := []string{
		"/f/" + f.token,
		"/f/" + f.token + "/n/projects/plan-note",
		"/f/" + f.token + "/d/projects",
	}
	for _, path := range paths {
		resp := anonGet(t, f.client, path)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "the request must be the real route: %s", path)
	}

	out := buf.String()
	require.NotEmpty(t, out, "the access middleware logged nothing; the capture is broken, not the code")
	for _, path := range paths {
		assert.NotContains(t, out, path, "the access log must not contain the token verbatim")
	}
	assert.NotContains(t, out, f.token, "the token must not appear anywhere in the access log")

	// The log still has to be useful: the request is attributable to one link.
	handle := "/f/t_"
	assert.Contains(t, out, handle, "a redacted handle must still be logged")
	assert.Contains(t, out, "/n/projects/plan-note",
		"the deep path must survive redaction, or a note read is unloggable")
}

// ─── fixture inserts for the cap test ────────────────────────────────────────

// seedCappedNotes inserts n note rows into folderID directly, bypassing the API:
// the route under test is the listing, and 201 HTTP writes would measure the
// note-create path instead. Slugs are zero-padded so ORDER BY slug is numeric.
func seedCappedNotes(t *testing.T, pool *db.Pool, folderID int64, n int) error {
	t.Helper()

	var userID int64
	var diskPath string
	if err := pool.ReadDB.QueryRow(`SELECT user_id, disk_path FROM folders WHERE id = ?`, folderID).
		Scan(&userID, &diskPath); err != nil {
		return err
	}

	tx, err := pool.WriteDB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT INTO notes (user_id, folder_id, title, slug, disk_path, content, content_hash, tags)
		VALUES (?, ?, ?, ?, ?, '', '', '[]')`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	for i := 1; i <= n; i++ {
		slug := fmt.Sprintf("cap-%03d", i)
		title := fmt.Sprintf("Cap note %03d", i)
		if _, err := stmt.Exec(userID, folderID, title, slug, diskPath+"/"+slug+".md"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// seedCappedFolders inserts n subfolders with distinctive names, for the same
// reason and with the same ordering trick.
func seedCappedFolders(t *testing.T, pool *db.Pool, parentID int64, n int) error {

	var userID int64
	var diskPath string
	if err := pool.ReadDB.QueryRow(`SELECT user_id, disk_path FROM folders WHERE id = ?`, parentID).
		Scan(&userID, &diskPath); err != nil {
		return err
	}

	tx, err := pool.WriteDB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(
		`INSERT INTO folders (user_id, parent_id, name, disk_path) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("f-%03d", i)
		if _, err := stmt.Exec(userID, parentID, name, diskPath+"/"+name); err != nil {
			return err
		}
	}
	return tx.Commit()
}
