package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createNoteInFolder creates a note (optionally inside a folder) and writes its
// content via PATCH, returning the note id. Content is what seeds the link graph.
func createNoteInFolder(t *testing.T, c *testClient, title string, folderID *int64, content string) int64 {
	t.Helper()
	body := map[string]interface{}{"title": title}
	if folderID != nil {
		body["folder_id"] = *folderID
	}
	resp := c.do(t, http.MethodPost, "/api/v1/notes", body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var note map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&note))
	id := int64(note["id"].(float64))
	hash, _ := note["content_hash"].(string)

	if content != "" {
		pr := c.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/notes/%d", id), map[string]interface{}{
			"content":      content,
			"content_hash": hash,
		})
		defer pr.Body.Close()
		require.Equal(t, http.StatusOK, pr.StatusCode)
	}
	return id
}

func getJSONArray(t *testing.T, c *testClient, path string) []map[string]interface{} {
	t.Helper()
	resp := c.do(t, http.MethodGet, path, nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out []map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

func getLinks(t *testing.T, c *testClient, noteID int64) []map[string]interface{} {
	t.Helper()
	return getJSONArray(t, c, fmt.Sprintf("/api/v1/notes/%d/links", noteID))
}

func getBacklinks(t *testing.T, c *testClient, noteID int64) []map[string]interface{} {
	t.Helper()
	return getJSONArray(t, c, fmt.Sprintf("/api/v1/notes/%d/backlinks", noteID))
}

// ─── REST: GET /api/v1/notes/:id/links ────────────────────────────────────────

func TestHandler_Links_ResolvedUnresolvedAndAnchor(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	targetID := createNoteInFolder(t, c, "Target Page", nil, "")
	srcID := createNoteInFolder(t, c, "Source Page", nil,
		"see [[Target Page#Heading|the map]] and [[Missing Page]]")

	links := getLinks(t, c, srcID)
	require.Len(t, links, 2)

	var resolved, unresolved map[string]interface{}
	for _, l := range links {
		if l["to_note_id"] != nil {
			resolved = l
		} else {
			unresolved = l
		}
	}

	require.NotNil(t, resolved, "one edge should resolve")
	assert.Equal(t, float64(targetID), resolved["to_note_id"])
	assert.Equal(t, "Target Page", resolved["to_title"])
	assert.Equal(t, "Target Page", resolved["raw_target"])
	assert.Equal(t, "Heading", resolved["anchor"])
	assert.Equal(t, "the map", resolved["link_text"])
	assert.Equal(t, false, resolved["ambiguous"])
	assert.Equal(t, false, resolved["unresolved"])

	require.NotNil(t, unresolved, "one edge should be unresolved")
	assert.Equal(t, "Missing Page", unresolved["raw_target"])
	assert.Equal(t, true, unresolved["unresolved"])
	assert.Equal(t, false, unresolved["ambiguous"])
}

func TestHandler_Backlinks_ReturnsSources(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	targetID := createNoteInFolder(t, c, "Hub", nil, "")
	srcID := createNoteInFolder(t, c, "Spoke", nil, "links to [[Hub]]")

	back := getBacklinks(t, c, targetID)
	require.Len(t, back, 1)
	assert.Equal(t, float64(srcID), back[0]["from_note_id"])
	assert.Equal(t, "Spoke", back[0]["from_title"])

	// The target itself has no backlinks and no outgoing edges.
	assert.Empty(t, getLinks(t, c, targetID))
	assert.Empty(t, getBacklinks(t, c, srcID))
}

func TestHandler_Links_Ambiguous(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	fAlpha := createFolderHelper(t, c, "Alpha")
	fBeta := createFolderHelper(t, c, "Beta")
	createNoteInFolder(t, c, "Tie", &fAlpha, "")
	createNoteInFolder(t, c, "Tie", &fBeta, "")

	// Source at root: both "Tie" notes are one step from root → tie → ambiguous.
	srcID := createNoteInFolder(t, c, "Source", nil, "see [[Tie]]")

	links := getLinks(t, c, srcID)
	require.Len(t, links, 1)
	assert.Nil(t, links[0]["to_note_id"])
	assert.Equal(t, true, links[0]["ambiguous"])
	assert.Equal(t, false, links[0]["unresolved"])
}

func TestHandler_Links_NotFound(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/notes/999999/links", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_Links_InvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/notes/not-a-number/links", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_Backlinks_NotFound(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/notes/999999/backlinks", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// ─── Routing regression: new nested routes must not shadow note routes ────────

// Gin allows a static segment beside a param, but a mis-registered
// /notes/:id/links would either panic at registration or make /notes/:id
// swallow the suffix. Pin every sibling so a future route change can't quietly
// break the graph endpoints.
func TestRouter_NoteLinkRoutes_DoNotShadowNoteRoutes(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	noteID := createNoteInFolder(t, c, "Probe", nil, "self [[Probe]]")

	// Static siblings must still win over the :id param.
	for _, p := range []string{"/api/v1/notes/root", "/api/v1/notes/all", "/api/v1/notes/context"} {
		resp := c.do(t, http.MethodGet, p, nil)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "%s must not be shadowed by /:id", p)
	}

	// The bare param route must still resolve a real note.
	resp := c.do(t, http.MethodGet, fmt.Sprintf("/api/v1/notes/%d", noteID), nil)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "/api/v1/notes/:id must still work")

	// The new nested routes must be registered (not 404/no-route) and must not
	// be captured by the bare :id handler.
	for _, p := range []string{
		fmt.Sprintf("/api/v1/notes/%d/links", noteID),
		fmt.Sprintf("/api/v1/notes/%d/backlinks", noteID),
	} {
		gr := c.do(t, http.MethodGet, p, nil)
		gr.Body.Close()
		assert.Equal(t, http.StatusOK, gr.StatusCode, "%s must be registered", p)
	}
}
