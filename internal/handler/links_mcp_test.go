package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpToolCall performs a tools/call and returns the decoded JSON-RPC envelope.
func mcpToolCall(t *testing.T, m *mcpClient, sessionID string, id int, tool string, args map[string]interface{}) map[string]interface{} {
	t.Helper()
	resp := m.post(t, rpc(id, "tools/call", map[string]interface{}{
		"name":      tool,
		"arguments": args,
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	return result
}

// mcpToolText asserts no JSON-RPC error and returns the text payload.
func mcpToolText(t *testing.T, result map[string]interface{}) string {
	t.Helper()
	require.Nil(t, result["error"], "unexpected JSON-RPC error: %v", result["error"])
	return result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
}

// createScopedToken creates a readwrite API token restricted to one folder and
// returns the raw bearer token.
func createScopedToken(t *testing.T, c *testClient, folderID int64, perm string) string {
	t.Helper()
	resp := c.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{
		"name":  "scoped-links",
		"scope": "readwrite",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var created map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	tokenID := int64(created["id"].(float64))
	raw, _ := created["token"].(string)
	require.NotEmpty(t, raw)

	pr := c.do(t, http.MethodPut, fmt.Sprintf("/api/v1/account/tokens/%d/permissions", tokenID), map[string]interface{}{
		"folder_permissions": []map[string]interface{}{
			{"folder_id": folderID, "permission": perm},
		},
	})
	defer pr.Body.Close()
	require.Equal(t, http.StatusOK, pr.StatusCode)
	return raw
}

func TestMCP_ToolsCall_GetLinks_ResolvedAndBacklinks(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	targetID := createNoteInFolder(t, m.testClient, "Target Page", nil, "")
	srcID := createNoteInFolder(t, m.testClient, "Source Page", nil, "see [[Target Page]]")

	res := mcpToolCall(t, m, sessionID, 1, "get_links", map[string]interface{}{"id": srcID})
	var out struct {
		Outgoing  []map[string]interface{} `json:"outgoing"`
		Backlinks []map[string]interface{} `json:"backlinks"`
	}
	require.NoError(t, json.Unmarshal([]byte(mcpToolText(t, res)), &out))
	require.Len(t, out.Outgoing, 1)
	assert.Equal(t, float64(targetID), out.Outgoing[0]["id"])
	assert.Equal(t, "Target Page", out.Outgoing[0]["title"])
	assert.Equal(t, "Target Page", out.Outgoing[0]["target"])
	assert.Equal(t, false, out.Outgoing[0]["unresolved"])
	assert.Empty(t, out.Backlinks)

	res2 := mcpToolCall(t, m, sessionID, 2, "get_links", map[string]interface{}{"id": targetID})
	var out2 struct {
		Outgoing  []map[string]interface{} `json:"outgoing"`
		Backlinks []map[string]interface{} `json:"backlinks"`
	}
	require.NoError(t, json.Unmarshal([]byte(mcpToolText(t, res2)), &out2))
	require.Len(t, out2.Backlinks, 1)
	assert.Equal(t, float64(srcID), out2.Backlinks[0]["id"])
	assert.Equal(t, "Source Page", out2.Backlinks[0]["title"])
}

func TestMCP_ToolsCall_GetLinks_UnresolvedAndAmbiguous(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	// Unresolved: target has no matching note.
	srcID := createNoteInFolder(t, m.testClient, "Source", nil, "see [[Nowhere At All]]")
	res := mcpToolCall(t, m, sessionID, 1, "get_links", map[string]interface{}{"id": srcID})
	var p1 struct {
		Outgoing []map[string]interface{} `json:"outgoing"`
	}
	require.NoError(t, json.Unmarshal([]byte(mcpToolText(t, res)), &p1))
	require.Len(t, p1.Outgoing, 1)
	assert.Nil(t, p1.Outgoing[0]["id"])
	assert.Equal(t, true, p1.Outgoing[0]["unresolved"])
	assert.Equal(t, false, p1.Outgoing[0]["ambiguous"])

	// Ambiguous: two same-titled notes equidistant from a root-level source.
	fA := createFolderHelper(t, m.testClient, "Alpha")
	fB := createFolderHelper(t, m.testClient, "Beta")
	createNoteInFolder(t, m.testClient, "Tie", &fA, "")
	createNoteInFolder(t, m.testClient, "Tie", &fB, "")
	ambSrc := createNoteInFolder(t, m.testClient, "Amb Source", nil, "see [[Tie]]")

	res2 := mcpToolCall(t, m, sessionID, 2, "get_links", map[string]interface{}{"id": ambSrc})
	var p2 struct {
		Outgoing []map[string]interface{} `json:"outgoing"`
	}
	require.NoError(t, json.Unmarshal([]byte(mcpToolText(t, res2)), &p2))
	require.Len(t, p2.Outgoing, 1)
	assert.Nil(t, p2.Outgoing[0]["id"])
	assert.Equal(t, true, p2.Outgoing[0]["ambiguous"])
	assert.Equal(t, false, p2.Outgoing[0]["unresolved"])
}

func TestMCP_ToolsCall_GetLinks_MissingID(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	res := mcpToolCall(t, m, sessionID, 1, "get_links", map[string]interface{}{})
	require.NotNil(t, res["error"])
	assert.Equal(t, float64(-32602), res["error"].(map[string]interface{})["code"], "expected rpcInvalidParams")
}

// Security: a folder-scoped API token must not learn the titles of notes
// outside its permitted subtree through the link graph. Both directions are
// proven — an outgoing edge to an out-of-scope note is dropped, and a backlink
// from an out-of-scope note is dropped. A note entirely outside the scope is
// refused outright.
func TestMCP_GetLinks_ScopedToken_DropsOutOfScopeLinksAndBacklinks(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	allowed := createFolderHelper(t, c, "Allowed")
	denied := createFolderHelper(t, c, "Denied")

	// Targets first, so the sources' [[links]] resolve at index time (a note
	// created later does not retroactively resolve dangling links until the
	// next reconcile).
	insideTarget := createNoteInFolder(t, c, "Inside Target", &allowed, "")
	outsideTarget := createNoteInFolder(t, c, "Outside Target", &denied, "")

	// Inside the permitted folder: links to one in-scope and one out-of-scope note.
	insideSource := createNoteInFolder(t, c, "Inside Source", &allowed, "[[Inside Target]] [[Outside Target]]")

	// Outside the permitted folder: links INTO the in-scope note.
	_ = createNoteInFolder(t, c, "Outside Source", &denied, "[[Inside Target]]")

	raw := createScopedToken(t, c, allowed, "read")
	m := &mcpClient{testClient: c, bearerToken: raw}
	sessionID := m.initialize(t)

	// Outgoing from an in-scope note: the out-of-scope target edge is dropped.
	res := mcpToolCall(t, m, sessionID, 1, "get_links", map[string]interface{}{"id": insideSource})
	var out struct {
		Outgoing []map[string]interface{} `json:"outgoing"`
	}
	require.NoError(t, json.Unmarshal([]byte(mcpToolText(t, res)), &out))
	require.Len(t, out.Outgoing, 1, "only the in-scope target edge should survive")
	assert.Equal(t, float64(insideTarget), out.Outgoing[0]["id"])
	assert.Equal(t, "Inside Target", out.Outgoing[0]["title"])
	text := mcpToolText(t, res)
	assert.NotContains(t, text, "Outside Target", "out-of-scope target title must not leak")

	// Backlinks of an in-scope note: the out-of-scope source is dropped.
	res2 := mcpToolCall(t, m, sessionID, 2, "get_links", map[string]interface{}{"id": insideTarget})
	var back struct {
		Backlinks []map[string]interface{} `json:"backlinks"`
	}
	require.NoError(t, json.Unmarshal([]byte(mcpToolText(t, res2)), &back))
	require.Len(t, back.Backlinks, 1, "only the in-scope source backlink should survive")
	assert.Equal(t, float64(insideSource), back.Backlinks[0]["id"])
	assert.Equal(t, "Inside Source", back.Backlinks[0]["title"])
	assert.NotContains(t, mcpToolText(t, res2), "Outside Source", "out-of-scope source title must not leak")

	// A note entirely outside the token's scope is refused — and refused with
	// the same answer a note that does not exist gets, so the caller cannot
	// tell the two apart (finding N11).
	res3 := mcpToolCall(t, m, sessionID, 3, "get_links", map[string]interface{}{"id": outsideTarget})
	require.NotNil(t, res3["error"], "out-of-scope note read must be refused")
	errObj := res3["error"].(map[string]interface{})
	assert.Equal(t, float64(-32002), errObj["code"], "expected rpcErrNotFound")
	assert.Equal(t, "not found", errObj["message"], "out-of-scope and missing must share the message text")
}
