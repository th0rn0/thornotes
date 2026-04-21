package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpClient wraps testClient and adds bearer-token MCP helpers.
type mcpClient struct {
	*testClient
	bearerToken string
}

func newMCPClient(t *testing.T) *mcpClient {
	t.Helper()
	tc := newTestClient(t)
	tc.registerAndLogin(t)

	// Create an API token via the account endpoint.
	resp := tc.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]string{"name": "mcp-test"})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	raw, ok := result["token"].(string)
	require.True(t, ok, "expected string token in response")

	return &mcpClient{testClient: tc, bearerToken: raw}
}

// post sends a POST /mcp request with an optional session ID and returns the response.
func (m *mcpClient) post(t *testing.T, body interface{}, sessionID string) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, m.server.URL+"/mcp", bytes.NewReader(b))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.bearerToken)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := m.httpClient.Do(req)
	require.NoError(t, err)
	return resp
}

// doGET opens GET /mcp SSE endpoint with a 2s timeout and returns the response.
func (m *mcpClient) doGET(t *testing.T, sessionID string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.server.URL+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+m.bearerToken)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := m.httpClient.Do(req)
	if err != nil && resp != nil {
		// Context deadline fired after headers were received — that's fine.
		return resp
	}
	require.NoError(t, err)
	return resp
}

// doDelete sends DELETE /mcp with the given session ID.
func (m *mcpClient) doDelete(t *testing.T, sessionID string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, m.server.URL+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+m.bearerToken)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := m.httpClient.Do(req)
	require.NoError(t, err)
	return resp
}

// initialize calls the MCP initialize method and returns the assigned session ID.
func (m *mcpClient) initialize(t *testing.T) string {
	t.Helper()
	resp := m.post(t, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]interface{}{},
	}, "")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	sessionID := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "initialize must return Mcp-Session-Id header")
	return sessionID
}

// rpc is a shorthand for a single JSON-RPC 2.0 request map.
func rpc(id interface{}, method string, params interface{}) map[string]interface{} {
	m := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if id != nil {
		m["id"] = id
	}
	if params != nil {
		m["params"] = params
	}
	return m
}

// extractNoteID pulls the note ID from a successful tools/call create_note response.
func extractNoteID(t *testing.T, result map[string]interface{}) int64 {
	t.Helper()
	contentText := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var noteData map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(contentText), &noteData))
	return int64(noteData["id"].(float64))
}

// ─── Auth ─────────────────────────────────────────────────────────────────────

func TestMCP_POST_NoBearer(t *testing.T) {
	tc := newTestClient(t)
	tc.registerAndLogin(t)
	req, _ := http.NewRequest(http.MethodPost, tc.server.URL+"/mcp", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := tc.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMCP_GET_NoBearer(t *testing.T) {
	tc := newTestClient(t)
	tc.registerAndLogin(t)
	req, _ := http.NewRequest(http.MethodGet, tc.server.URL+"/mcp", nil)
	resp, err := tc.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMCP_DELETE_NoBearer(t *testing.T) {
	tc := newTestClient(t)
	tc.registerAndLogin(t)
	req, _ := http.NewRequest(http.MethodDelete, tc.server.URL+"/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "any-session")
	resp, err := tc.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// ─── Initialize ───────────────────────────────────────────────────────────────

func TestMCP_Initialize(t *testing.T) {
	m := newMCPClient(t)
	resp := m.post(t, rpc(1, "initialize", map[string]interface{}{}), "")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	sessionID := resp.Header.Get("Mcp-Session-Id")
	assert.NotEmpty(t, sessionID)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "2.0", result["jsonrpc"])
	res := result["result"].(map[string]interface{})
	assert.Equal(t, "2025-03-26", res["protocolVersion"])
	info := res["serverInfo"].(map[string]interface{})
	assert.Equal(t, "thornotes", info["name"])
}

// ─── Notifications → 202 ─────────────────────────────────────────────────────

func TestMCP_Notification_Returns202(t *testing.T) {
	m := newMCPClient(t)
	// A notification has no "id" field.
	resp := m.post(t, map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}, "")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

func TestMCP_Notification_NullID_Returns202(t *testing.T) {
	m := newMCPClient(t)
	resp := m.post(t, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      nil,
		"method":  "notifications/initialized",
	}, "")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

// ─── Parse / validation errors ────────────────────────────────────────────────

func TestMCP_POST_EmptyBody(t *testing.T) {
	m := newMCPClient(t)
	req, _ := http.NewRequest(http.MethodPost, m.server.URL+"/mcp", bytes.NewReader([]byte("")))
	req.Header.Set("Authorization", "Bearer "+m.bearerToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotNil(t, result["error"])
}

func TestMCP_POST_InvalidJSON(t *testing.T) {
	m := newMCPClient(t)
	req, _ := http.NewRequest(http.MethodPost, m.server.URL+"/mcp", bytes.NewReader([]byte("{bad json")))
	req.Header.Set("Authorization", "Bearer "+m.bearerToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32700), errObj["code"])
}

func TestMCP_POST_WrongJSONRPCVersion(t *testing.T) {
	m := newMCPClient(t)
	resp := m.post(t, map[string]interface{}{
		"jsonrpc": "1.0",
		"id":      1,
		"method":  "initialize",
	}, "")
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32600), errObj["code"])
}

// ─── Session validation ───────────────────────────────────────────────────────

func TestMCP_InvalidSessionID_Returns404(t *testing.T) {
	m := newMCPClient(t)
	resp := m.post(t, rpc(1, "tools/list", nil), "nonexistent-session-id")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestMCP_ValidSessionID_Accepted(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(2, "tools/list", nil), sessionID)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// ─── tools/list ───────────────────────────────────────────────────────────────

func TestMCP_ToolsList(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(2, "tools/list", nil), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	res := result["result"].(map[string]interface{})
	tools := res["tools"].([]interface{})
	assert.NotEmpty(t, tools)

	// Verify all expected tools are present.
	names := make(map[string]bool)
	for _, t2 := range tools {
		tool := t2.(map[string]interface{})
		names[tool["name"].(string)] = true
	}
	for _, want := range []string{"list_notes", "get_note", "search_notes", "list_folders", "create_note", "update_note"} {
		assert.True(t, names[want], "expected tool %q in list", want)
	}
}

// ─── tools/call ──────────────────────────────────────────────────────────────

func TestMCP_ToolsCall_ListFolders(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(3, "tools/call", map[string]interface{}{
		"name":      "list_folders",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
}

func TestMCP_ToolsCall_CreateNote(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(4, "tools/call", map[string]interface{}{
		"name": "create_note",
		"arguments": map[string]interface{}{
			"title":   "MCP Test Note",
			"content": "# Hello from MCP",
		},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
	res := result["result"].(map[string]interface{})
	contents := res["content"].([]interface{})
	assert.NotEmpty(t, contents)
}

func TestMCP_ToolsCall_GetNote(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	// Create a note first.
	createResp := m.post(t, rpc(5, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "GetNoteTest"},
	}), sessionID)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	var createResult map[string]interface{}
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&createResult))
	noteID := extractNoteID(t, createResult)

	// Now get it.
	getResp := m.post(t, rpc(6, "tools/call", map[string]interface{}{
		"name":      "get_note",
		"arguments": map[string]interface{}{"id": noteID},
	}), sessionID)
	defer getResp.Body.Close()
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	var getResult map[string]interface{}
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&getResult))
	assert.Nil(t, getResult["error"])
}

func TestMCP_ToolsCall_UpdateNote(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	createResp := m.post(t, rpc(7, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "UpdateTest"},
	}), sessionID)
	defer createResp.Body.Close()
	var createResult map[string]interface{}
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&createResult))
	noteID := extractNoteID(t, createResult)

	updateResp := m.post(t, rpc(8, "tools/call", map[string]interface{}{
		"name": "update_note",
		"arguments": map[string]interface{}{
			"id":      noteID,
			"content": "# Updated content",
		},
	}), sessionID)
	defer updateResp.Body.Close()
	require.Equal(t, http.StatusOK, updateResp.StatusCode)

	var updateResult map[string]interface{}
	require.NoError(t, json.NewDecoder(updateResp.Body).Decode(&updateResult))
	assert.Nil(t, updateResult["error"])
}

func TestMCP_ToolsCall_ListNotes(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	// Create a note first.
	cResp := m.post(t, rpc(9, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "ListTest"},
	}), sessionID)
	cResp.Body.Close()

	resp := m.post(t, rpc(10, "tools/call", map[string]interface{}{
		"name":      "list_notes",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
}

func TestMCP_ToolsCall_SearchNotes(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(11, "tools/call", map[string]interface{}{
		"name":      "search_notes",
		"arguments": map[string]interface{}{"query": "hello"},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
}

func TestMCP_ToolsCall_SearchNotes_MissingQuery(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(12, "tools/call", map[string]interface{}{
		"name":      "search_notes",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

func TestMCP_ToolsCall_GetNote_MissingID(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(13, "tools/call", map[string]interface{}{
		"name":      "get_note",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

func TestMCP_ToolsCall_CreateNote_MissingTitle(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(14, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

func TestMCP_ToolsCall_UnknownTool(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(15, "tools/call", map[string]interface{}{
		"name":      "nonexistent_tool",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32601), errObj["code"])
}

// ─── resources/list and resources/read ───────────────────────────────────────

func TestMCP_ResourcesList(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(16, "resources/list", nil), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
	res := result["result"].(map[string]interface{})
	assert.NotNil(t, res["resources"])
}

func TestMCP_ResourcesRead(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	// Create a note via tool.
	cResp := m.post(t, rpc(17, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "ResourceTest", "content": "hello"},
	}), sessionID)
	defer cResp.Body.Close()
	var cResult map[string]interface{}
	require.NoError(t, json.NewDecoder(cResp.Body).Decode(&cResult))
	noteID := extractNoteID(t, cResult)

	resp := m.post(t, rpc(18, "resources/read", map[string]interface{}{
		"uri": fmt.Sprintf("note://%d", noteID),
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
}

func TestMCP_ResourcesRead_InvalidURI(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(19, "resources/read", map[string]interface{}{
		"uri": "bad://123",
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

// ─── Method not found ─────────────────────────────────────────────────────────

func TestMCP_UnknownMethod(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(20, "unknown/method", nil), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32601), errObj["code"])
}

// ─── Batch requests ───────────────────────────────────────────────────────────

func TestMCP_Batch_AllNotifications_Returns202(t *testing.T) {
	m := newMCPClient(t)
	batch := []map[string]interface{}{
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
		{"jsonrpc": "2.0", "method": "notifications/ping"},
	}
	resp := m.post(t, batch, "")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

func TestMCP_Batch_MixedRequests(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	batch := []map[string]interface{}{
		{"jsonrpc": "2.0", "method": "notifications/ping"},
		rpc(21, "tools/list", nil),
	}
	resp := m.post(t, batch, sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var results []map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&results))
	assert.Len(t, results, 1)
	assert.Equal(t, float64(21), results[0]["id"])
}

func TestMCP_Batch_InvalidJSON(t *testing.T) {
	m := newMCPClient(t)
	req, _ := http.NewRequest(http.MethodPost, m.server.URL+"/mcp", bytes.NewReader([]byte("[bad")))
	req.Header.Set("Authorization", "Bearer "+m.bearerToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32700), errObj["code"])
}

func TestMCP_Batch_EmptyArray(t *testing.T) {
	m := newMCPClient(t)
	resp := m.post(t, []interface{}{}, "")
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32600), errObj["code"])
}

// ─── GET /mcp SSE ─────────────────────────────────────────────────────────────

func TestMCP_GET_NoSession_Opens(t *testing.T) {
	m := newMCPClient(t)
	resp := m.doGET(t, "")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
}

func TestMCP_GET_InvalidSession_404(t *testing.T) {
	m := newMCPClient(t)
	resp := m.doGET(t, "invalid-session-id")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestMCP_GET_ValidSession_Opens(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)
	resp := m.doGET(t, sessionID)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// ─── Error paths ─────────────────────────────────────────────────────────────

func TestMCP_ToolsCall_GetNote_NotFound(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(23, "tools/call", map[string]interface{}{
		"name":      "get_note",
		"arguments": map[string]interface{}{"id": 999999},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	// Should return an RPC-level error, not a transport error.
	assert.NotNil(t, result["error"])
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32603), errObj["code"])
}

func TestMCP_ToolsCall_UpdateNote_NotFound(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(24, "tools/call", map[string]interface{}{
		"name": "update_note",
		"arguments": map[string]interface{}{
			"id":      999999,
			"content": "new content",
		},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotNil(t, result["error"])
}

// ─── DELETE /mcp ─────────────────────────────────────────────────────────────

func TestMCP_DELETE_ValidSession(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.doDelete(t, sessionID)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Session is now gone — subsequent requests should get 404.
	resp2 := m.post(t, rpc(22, "tools/list", nil), sessionID)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp2.StatusCode)
}

func TestMCP_DELETE_NoSessionID(t *testing.T) {
	m := newMCPClient(t)
	resp := m.doDelete(t, "")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestMCP_DELETE_InvalidSession(t *testing.T) {
	m := newMCPClient(t)
	resp := m.doDelete(t, "nonexistent")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// ─── Additional edge cases ────────────────────────────────────────────────────

func TestMCP_ToolsCall_UpdateNote_EmptyContent(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	cResp := m.post(t, rpc(25, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "EmptyContentTest"},
	}), sessionID)
	defer cResp.Body.Close()
	var cResult map[string]interface{}
	require.NoError(t, json.NewDecoder(cResp.Body).Decode(&cResult))
	noteID := extractNoteID(t, cResult)

	resp := m.post(t, rpc(26, "tools/call", map[string]interface{}{
		"name": "update_note",
		"arguments": map[string]interface{}{
			"id":      noteID,
			"content": "",
		},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

func TestMCP_ResourcesRead_NotFound(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(27, "resources/read", map[string]interface{}{
		"uri": "note://999999",
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotNil(t, result["error"])
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32603), errObj["code"])
}

func TestMCP_ResourcesRead_NonNumericID(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(28, "resources/read", map[string]interface{}{
		"uri": "note://abc",
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

func TestMCP_ToolsCall_ListNotes_WithFolderID(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	// Create a note first to ensure something exists.
	cResp := m.post(t, rpc(29, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "FolderListTest"},
	}), sessionID)
	cResp.Body.Close()

	// folder_id=0 is not a valid folder — expect list to succeed (empty or data).
	// Use a nil-like approach: pass folder_id explicitly as integer.
	resp := m.post(t, rpc(30, "tools/call", map[string]interface{}{
		"name": "list_notes",
		"arguments": map[string]interface{}{
			"folder_id": float64(0),
		},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	// folder_id 0 may return empty or error — just verify no panic/500.
	assert.NotNil(t, result)
}

func TestMCP_Batch_InvalidSessionPerItem(t *testing.T) {
	m := newMCPClient(t)

	batch := []map[string]interface{}{
		rpc(31, "tools/list", nil),
	}
	resp := m.post(t, batch, "bogus-session-id")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var results []map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&results))
	require.Len(t, results, 1)
	errObj := results[0]["error"].(map[string]interface{})
	assert.Equal(t, float64(-32001), errObj["code"])
}

// ─── find_folders ─────────────────────────────────────────────────────────────

func TestMCP_ToolsCall_FindFolders_ReturnsMatch(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	// Create a folder via the REST API.
	resp := m.post(t, rpc(40, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "Note in Work"},
	}), sessionID)
	resp.Body.Close()

	// Create folder via REST (MCP has no create_folder tool).
	folderResp := m.do(t, http.MethodPost, "/api/v1/folders", map[string]string{"name": "WorkFolder"})
	folderResp.Body.Close()

	resp = m.post(t, rpc(41, "tools/call", map[string]interface{}{
		"name":      "find_folders",
		"arguments": map[string]interface{}{"query": "work"},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var folders []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &folders))
	require.Len(t, folders, 1)
	assert.Equal(t, "WorkFolder", folders[0]["name"])
}

func TestMCP_ToolsCall_FindFolders_MissingQuery(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(42, "tools/call", map[string]interface{}{
		"name":      "find_folders",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

func TestMCP_ToolsCall_FindFolders_NoMatch(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(43, "tools/call", map[string]interface{}{
		"name":      "find_folders",
		"arguments": map[string]interface{}{"query": "zzznomatch"},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	assert.Equal(t, "null", text)
}

// ─── find_notes_by_tag ────────────────────────────────────────────────────────

func TestMCP_ToolsCall_FindNotesByTag_ReturnsMatch(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	// Create a tagged note.
	cResp := m.post(t, rpc(44, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "Tagged Note", "tags": []string{"go", "testing"}},
	}), sessionID)
	cResp.Body.Close()
	// Create an untagged note.
	cResp2 := m.post(t, rpc(45, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "Untagged Note"},
	}), sessionID)
	cResp2.Body.Close()

	resp := m.post(t, rpc(46, "tools/call", map[string]interface{}{
		"name":      "find_notes_by_tag",
		"arguments": map[string]interface{}{"tags": []string{"go"}},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var notes []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &notes))
	require.Len(t, notes, 1)
	assert.Equal(t, "Tagged Note", notes[0]["title"])
}

func TestMCP_ToolsCall_FindNotesByTag_ANDSemantics(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	cResp := m.post(t, rpc(47, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "Both Tags", "tags": []string{"alpha", "beta"}},
	}), sessionID)
	cResp.Body.Close()
	cResp2 := m.post(t, rpc(48, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "Only Alpha", "tags": []string{"alpha"}},
	}), sessionID)
	cResp2.Body.Close()

	// Asking for both tags should return only "Both Tags".
	resp := m.post(t, rpc(49, "tools/call", map[string]interface{}{
		"name":      "find_notes_by_tag",
		"arguments": map[string]interface{}{"tags": []string{"alpha", "beta"}},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var notes []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &notes))
	require.Len(t, notes, 1)
	assert.Equal(t, "Both Tags", notes[0]["title"])
}

func TestMCP_ToolsCall_FindNotesByTag_EmptyTagsReturnsError(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(50, "tools/call", map[string]interface{}{
		"name":      "find_notes_by_tag",
		"arguments": map[string]interface{}{"tags": []string{}},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

// ─── list_tags ────────────────────────────────────────────────────────────────

func TestMCP_ToolsCall_ListTags_ReturnsAllTags(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	cResp := m.post(t, rpc(51, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "Note A", "tags": []string{"zeta", "alpha"}},
	}), sessionID)
	cResp.Body.Close()
	cResp2 := m.post(t, rpc(52, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "Note B", "tags": []string{"beta", "alpha"}},
	}), sessionID)
	cResp2.Body.Close()

	resp := m.post(t, rpc(53, "tools/call", map[string]interface{}{
		"name":      "list_tags",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var tags []string
	require.NoError(t, json.Unmarshal([]byte(text), &tags))
	assert.Equal(t, []string{"alpha", "beta", "zeta"}, tags) // sorted, deduplicated
}

func TestMCP_ToolsCall_ListTags_EmptyWhenNoNotes(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(54, "tools/call", map[string]interface{}{
		"name":      "list_tags",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	// No notes → empty array or null.
	assert.True(t, text == "[]" || text == "null", "expected empty list, got: %s", text)
}

// ─── resources/list with tagged notes ─────────────────────────────────────────

func TestMCP_ResourcesList_WithTaggedNote(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	// Create a note with tags so the description branch is exercised.
	cResp := m.post(t, rpc(60, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "Tagged Note", "tags": []string{"foo", "bar"}},
	}), sessionID)
	cResp.Body.Close()

	resp := m.post(t, rpc(61, "resources/list", nil), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
	resources := result["result"].(map[string]interface{})["resources"].([]interface{})
	// At least the tagged note must appear; auto-created notes may also be present.
	found := false
	for _, r := range resources {
		desc, _ := r.(map[string]interface{})["description"].(string)
		if strings.Contains(desc, "foo") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected a resource with tag 'foo' in description")
}

// ─── Read-only scope blocks write tools ───────────────────────────────────────

func newReadOnlyMCPClient(t *testing.T) *mcpClient {
	t.Helper()
	tc := newTestClient(t)
	tc.registerAndLogin(t)

	resp := tc.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]string{"name": "ro-mcp", "scope": "read"})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	raw, ok := result["token"].(string)
	require.True(t, ok)
	return &mcpClient{testClient: tc, bearerToken: raw}
}

func TestMCP_ToolsCall_ReadOnly_BlocksWriteTool(t *testing.T) {
	m := newReadOnlyMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(62, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "Blocked"},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32001), errObj["code"])
	assert.Contains(t, errObj["message"].(string), "read-only")
}

// ─── rename_note ──────────────────────────────────────────────────────────────

func TestMCP_ToolsCall_RenameNote_Success(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	cResp := m.post(t, rpc(63, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "Old Title"},
	}), sessionID)
	defer cResp.Body.Close()
	var cResult map[string]interface{}
	require.NoError(t, json.NewDecoder(cResp.Body).Decode(&cResult))
	noteID := extractNoteID(t, cResult)

	newTitle := "New Title"
	resp := m.post(t, rpc(64, "tools/call", map[string]interface{}{
		"name": "rename_note",
		"arguments": map[string]interface{}{
			"id":    noteID,
			"title": newTitle,
		},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	assert.Contains(t, text, "New Title")
}

func TestMCP_ToolsCall_RenameNote_MissingID(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(65, "tools/call", map[string]interface{}{
		"name":      "rename_note",
		"arguments": map[string]interface{}{"title": "X"},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

func TestMCP_ToolsCall_RenameNote_NoTitleOrTags(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	cResp := m.post(t, rpc(66, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "Note"},
	}), sessionID)
	defer cResp.Body.Close()
	var cResult map[string]interface{}
	require.NoError(t, json.NewDecoder(cResp.Body).Decode(&cResult))
	noteID := extractNoteID(t, cResult)

	resp := m.post(t, rpc(67, "tools/call", map[string]interface{}{
		"name":      "rename_note",
		"arguments": map[string]interface{}{"id": noteID},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
	assert.Contains(t, errObj["message"].(string), "title or tags")
}

// ─── move_note ────────────────────────────────────────────────────────────────

func TestMCP_ToolsCall_MoveNote_ToRoot(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	// Create folder via REST.
	folderResp := m.do(t, http.MethodPost, "/api/v1/folders",
		map[string]interface{}{"name": "MCP Dest"})
	defer folderResp.Body.Close()
	var folder map[string]interface{}
	require.NoError(t, json.NewDecoder(folderResp.Body).Decode(&folder))
	folderID := int64(folder["id"].(float64))

	cResp := m.post(t, rpc(68, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "MoveMe", "folder_id": folderID},
	}), sessionID)
	defer cResp.Body.Close()
	var cResult map[string]interface{}
	require.NoError(t, json.NewDecoder(cResp.Body).Decode(&cResult))
	noteID := extractNoteID(t, cResult)

	resp := m.post(t, rpc(69, "tools/call", map[string]interface{}{
		"name":      "move_note",
		"arguments": map[string]interface{}{"id": noteID},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	assert.Contains(t, text, "root")
}

func TestMCP_ToolsCall_MoveNote_MissingID(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(70, "tools/call", map[string]interface{}{
		"name":      "move_note",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

// ─── delete_note ──────────────────────────────────────────────────────────────

func TestMCP_ToolsCall_DeleteNote_Success(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	cResp := m.post(t, rpc(71, "tools/call", map[string]interface{}{
		"name":      "create_note",
		"arguments": map[string]interface{}{"title": "ToDelete"},
	}), sessionID)
	defer cResp.Body.Close()
	var cResult map[string]interface{}
	require.NoError(t, json.NewDecoder(cResp.Body).Decode(&cResult))
	noteID := extractNoteID(t, cResult)

	resp := m.post(t, rpc(72, "tools/call", map[string]interface{}{
		"name":      "delete_note",
		"arguments": map[string]interface{}{"id": noteID},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	assert.Contains(t, text, "deleted")
}

func TestMCP_ToolsCall_DeleteNote_MissingID(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(73, "tools/call", map[string]interface{}{
		"name":      "delete_note",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

// ─── create_folder ────────────────────────────────────────────────────────────

func TestMCP_ToolsCall_CreateFolder_Success(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(74, "tools/call", map[string]interface{}{
		"name":      "create_folder",
		"arguments": map[string]interface{}{"name": "MCP Folder"},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	assert.Contains(t, text, "MCP Folder")
}

func TestMCP_ToolsCall_CreateFolder_MissingName(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(75, "tools/call", map[string]interface{}{
		"name":      "create_folder",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

// ─── rename_folder ────────────────────────────────────────────────────────────

func TestMCP_ToolsCall_RenameFolder_Success(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	fResp := m.post(t, rpc(76, "tools/call", map[string]interface{}{
		"name":      "create_folder",
		"arguments": map[string]interface{}{"name": "OldFolder"},
	}), sessionID)
	defer fResp.Body.Close()
	var fResult map[string]interface{}
	require.NoError(t, json.NewDecoder(fResp.Body).Decode(&fResult))
	folderText := fResult["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var folderData map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(folderText), &folderData))
	folderID := int64(folderData["id"].(float64))

	resp := m.post(t, rpc(77, "tools/call", map[string]interface{}{
		"name":      "rename_folder",
		"arguments": map[string]interface{}{"id": folderID, "name": "NewFolder"},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	assert.Contains(t, text, "NewFolder")
}

func TestMCP_ToolsCall_RenameFolder_MissingParams(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(78, "tools/call", map[string]interface{}{
		"name":      "rename_folder",
		"arguments": map[string]interface{}{"id": 0, "name": ""},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

// ─── move_folder ──────────────────────────────────────────────────────────────

func TestMCP_ToolsCall_MoveFolder_ToParent(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	parentResp := m.post(t, rpc(79, "tools/call", map[string]interface{}{
		"name":      "create_folder",
		"arguments": map[string]interface{}{"name": "Parent"},
	}), sessionID)
	defer parentResp.Body.Close()
	var parentResult map[string]interface{}
	require.NoError(t, json.NewDecoder(parentResp.Body).Decode(&parentResult))
	parentText := parentResult["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var parentData map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(parentText), &parentData))
	parentID := int64(parentData["id"].(float64))

	childResp := m.post(t, rpc(80, "tools/call", map[string]interface{}{
		"name":      "create_folder",
		"arguments": map[string]interface{}{"name": "Child"},
	}), sessionID)
	defer childResp.Body.Close()
	var childResult map[string]interface{}
	require.NoError(t, json.NewDecoder(childResp.Body).Decode(&childResult))
	childText := childResult["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var childData map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(childText), &childData))
	childID := int64(childData["id"].(float64))

	resp := m.post(t, rpc(81, "tools/call", map[string]interface{}{
		"name":      "move_folder",
		"arguments": map[string]interface{}{"id": childID, "parent_id": parentID},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	assert.Contains(t, text, "moved")
}

func TestMCP_ToolsCall_MoveFolder_MissingID(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(82, "tools/call", map[string]interface{}{
		"name":      "move_folder",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

// ─── delete_folder ────────────────────────────────────────────────────────────

func TestMCP_ToolsCall_DeleteFolder_Success(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	fResp := m.post(t, rpc(83, "tools/call", map[string]interface{}{
		"name":      "create_folder",
		"arguments": map[string]interface{}{"name": "ToDeleteFolder"},
	}), sessionID)
	defer fResp.Body.Close()
	var fResult map[string]interface{}
	require.NoError(t, json.NewDecoder(fResp.Body).Decode(&fResult))
	folderText := fResult["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var folderData map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(folderText), &folderData))
	folderID := int64(folderData["id"].(float64))

	resp := m.post(t, rpc(84, "tools/call", map[string]interface{}{
		"name":      "delete_folder",
		"arguments": map[string]interface{}{"id": folderID},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	assert.Contains(t, text, "deleted")
}

func TestMCP_ToolsCall_DeleteFolder_MissingID(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(85, "tools/call", map[string]interface{}{
		"name":      "delete_folder",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	errObj := result["error"].(map[string]interface{})
	assert.Equal(t, float64(-32602), errObj["code"])
}

// ─── service error paths via MCP ──────────────────────────────────────────────

func TestMCP_ToolsCall_DeleteNote_NotFound(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(86, "tools/call", map[string]interface{}{
		"name":      "delete_note",
		"arguments": map[string]interface{}{"id": 999999},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotNil(t, result["error"])
}

func TestMCP_ToolsCall_DeleteFolder_NotFound(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(87, "tools/call", map[string]interface{}{
		"name":      "delete_folder",
		"arguments": map[string]interface{}{"id": 999999},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotNil(t, result["error"])
}

func TestMCP_ToolsCall_RenameNote_NotFound(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	newTitle := "New"
	resp := m.post(t, rpc(88, "tools/call", map[string]interface{}{
		"name":      "rename_note",
		"arguments": map[string]interface{}{"id": 999999, "title": &newTitle},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotNil(t, result["error"])
}

func TestMCP_ToolsCall_MoveNote_NotFound(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(89, "tools/call", map[string]interface{}{
		"name":      "move_note",
		"arguments": map[string]interface{}{"id": 999999},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotNil(t, result["error"])
}

func TestMCP_ToolsCall_RenameFolder_NotFound(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(90, "tools/call", map[string]interface{}{
		"name":      "rename_folder",
		"arguments": map[string]interface{}{"id": 999999, "name": "X"},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotNil(t, result["error"])
}

func TestMCP_ToolsCall_MoveFolder_NotFound(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	resp := m.post(t, rpc(91, "tools/call", map[string]interface{}{
		"name":      "move_folder",
		"arguments": map[string]interface{}{"id": 999999},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotNil(t, result["error"])
}

func TestMCP_ToolsCall_UpdateNote_NotFoundError(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	// update_note GetNote path for non-existent note.
	resp := m.post(t, rpc(92, "tools/call", map[string]interface{}{
		"name":      "update_note",
		"arguments": map[string]interface{}{"id": 999999, "content": "# x"},
	}), sessionID)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotNil(t, result["error"])
}

func TestMCP_ToolsCall_CreateNote_WithContent_ContentUpdateError(t *testing.T) {
	m := newMCPClient(t)
	sessionID := m.initialize(t)

	// Create a note with content — exercises the UpdateNoteContent path inside create_note.
	resp := m.post(t, rpc(93, "tools/call", map[string]interface{}{
		"name": "create_note",
		"arguments": map[string]interface{}{
			"title":   "With Content",
			"content": "# Hello World",
		},
	}), sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Nil(t, result["error"])
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	assert.Contains(t, text, "With Content")
}

// ─── Folder-scoped permissions ────────────────────────────────────────────────

func TestMCP_Scoped_WriteInsideGrantedFolder(t *testing.T) {
	// Build a folder, grant write, and verify create_note succeeds inside it.
	tc := newTestClient(t)
	tc.registerAndLogin(t)
	workID := createFolderHelper(t, tc, "Work")

	// Create a token and scope it to Work:write.
	resp := tc.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{
		"name":  "scoped",
		"scope": "readwrite",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var created map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	tokenID := int64(created["id"].(float64))
	raw := created["token"].(string)

	permResp := tc.do(t, http.MethodPut, fmt.Sprintf("/api/v1/account/tokens/%d/permissions", tokenID), map[string]interface{}{
		"folder_permissions": []map[string]interface{}{
			{"folder_id": workID, "permission": "write"},
		},
	})
	defer permResp.Body.Close()
	require.Equal(t, http.StatusOK, permResp.StatusCode)

	m := &mcpClient{testClient: tc, bearerToken: raw}
	sessionID := m.initialize(t)

	// Write inside Work should succeed.
	cr := m.post(t, rpc(1, "tools/call", map[string]interface{}{
		"name": "create_note",
		"arguments": map[string]interface{}{
			"title":     "inside",
			"folder_id": workID,
		},
	}), sessionID)
	defer cr.Body.Close()
	var cres map[string]interface{}
	require.NoError(t, json.NewDecoder(cr.Body).Decode(&cres))
	assert.Nil(t, cres["error"], "expected create inside scoped folder to succeed: %v", cres["error"])
}

func TestMCP_Scoped_WriteOutsideGrantedFolderIsDenied(t *testing.T) {
	tc := newTestClient(t)
	tc.registerAndLogin(t)
	workID := createFolderHelper(t, tc, "Work")
	privateID := createFolderHelper(t, tc, "Private")

	resp := tc.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{
		"name":  "scoped",
		"scope": "readwrite",
	})
	defer resp.Body.Close()
	var created map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	tokenID := int64(created["id"].(float64))
	raw := created["token"].(string)

	permResp := tc.do(t, http.MethodPut, fmt.Sprintf("/api/v1/account/tokens/%d/permissions", tokenID), map[string]interface{}{
		"folder_permissions": []map[string]interface{}{
			{"folder_id": workID, "permission": "write"},
		},
	})
	defer permResp.Body.Close()
	require.Equal(t, http.StatusOK, permResp.StatusCode)

	m := &mcpClient{testClient: tc, bearerToken: raw}
	sessionID := m.initialize(t)

	// Creating in Private must be rejected.
	cr := m.post(t, rpc(1, "tools/call", map[string]interface{}{
		"name": "create_note",
		"arguments": map[string]interface{}{
			"title":     "nope",
			"folder_id": privateID,
		},
	}), sessionID)
	defer cr.Body.Close()
	var cres map[string]interface{}
	require.NoError(t, json.NewDecoder(cr.Body).Decode(&cres))
	errObj, _ := cres["error"].(map[string]interface{})
	require.NotNil(t, errObj, "expected error when writing outside scope")
	assert.Equal(t, float64(-32001), errObj["code"])
	assert.Contains(t, errObj["message"].(string), "write access")

	// Creating at root must also be rejected (no root grant).
	rr := m.post(t, rpc(2, "tools/call", map[string]interface{}{
		"name": "create_note",
		"arguments": map[string]interface{}{
			"title": "root-nope",
		},
	}), sessionID)
	defer rr.Body.Close()
	var rres map[string]interface{}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&rres))
	assert.NotNil(t, rres["error"])
}

func TestMCP_Scoped_ReadGrant_BlocksWriteButAllowsRead(t *testing.T) {
	tc := newTestClient(t)
	tc.registerAndLogin(t)
	workID := createFolderHelper(t, tc, "Work")

	// Seed a note in Work via session auth.
	nResp := tc.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{
		"title":     "seed",
		"folder_id": workID,
	})
	defer nResp.Body.Close()
	require.Equal(t, http.StatusCreated, nResp.StatusCode)
	var seed map[string]interface{}
	require.NoError(t, json.NewDecoder(nResp.Body).Decode(&seed))
	noteID := int64(seed["id"].(float64))

	// Create a read-scoped token and grant read on Work.
	resp := tc.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{
		"name":  "ro",
		"scope": "readwrite",
	})
	defer resp.Body.Close()
	var created map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	tokenID := int64(created["id"].(float64))
	raw := created["token"].(string)

	permResp := tc.do(t, http.MethodPut, fmt.Sprintf("/api/v1/account/tokens/%d/permissions", tokenID), map[string]interface{}{
		"folder_permissions": []map[string]interface{}{
			{"folder_id": workID, "permission": "read"},
		},
	})
	defer permResp.Body.Close()
	require.Equal(t, http.StatusOK, permResp.StatusCode)

	m := &mcpClient{testClient: tc, bearerToken: raw}
	sessionID := m.initialize(t)

	// get_note must succeed.
	gr := m.post(t, rpc(1, "tools/call", map[string]interface{}{
		"name":      "get_note",
		"arguments": map[string]interface{}{"id": noteID},
	}), sessionID)
	defer gr.Body.Close()
	var gres map[string]interface{}
	require.NoError(t, json.NewDecoder(gr.Body).Decode(&gres))
	assert.Nil(t, gres["error"], "read-granted folder should allow get_note")

	// update_note must be denied.
	ur := m.post(t, rpc(2, "tools/call", map[string]interface{}{
		"name": "update_note",
		"arguments": map[string]interface{}{
			"id":      noteID,
			"content": "banned",
		},
	}), sessionID)
	defer ur.Body.Close()
	var ures map[string]interface{}
	require.NoError(t, json.NewDecoder(ur.Body).Decode(&ures))
	errObj, _ := ures["error"].(map[string]interface{})
	require.NotNil(t, errObj)
	assert.Equal(t, float64(-32001), errObj["code"])
}

func TestMCP_Scoped_ListNotesFilteredByGrant(t *testing.T) {
	tc := newTestClient(t)
	tc.registerAndLogin(t)
	workID := createFolderHelper(t, tc, "Work")
	privateID := createFolderHelper(t, tc, "Private")

	// Seed one note per folder.
	for _, fid := range []int64{workID, privateID} {
		r := tc.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{
			"title":     fmt.Sprintf("note-%d", fid),
			"folder_id": fid,
		})
		require.Equal(t, http.StatusCreated, r.StatusCode)
		r.Body.Close()
	}

	// Token scoped to Work:read.
	resp := tc.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{
		"name":  "ro",
		"scope": "readwrite",
	})
	defer resp.Body.Close()
	var created map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	tokenID := int64(created["id"].(float64))
	raw := created["token"].(string)

	permResp := tc.do(t, http.MethodPut, fmt.Sprintf("/api/v1/account/tokens/%d/permissions", tokenID), map[string]interface{}{
		"folder_permissions": []map[string]interface{}{
			{"folder_id": workID, "permission": "read"},
		},
	})
	defer permResp.Body.Close()
	require.Equal(t, http.StatusOK, permResp.StatusCode)

	m := &mcpClient{testClient: tc, bearerToken: raw}
	sessionID := m.initialize(t)

	// list_notes without folder_id should return only the Work note.
	lr := m.post(t, rpc(1, "tools/call", map[string]interface{}{
		"name":      "list_notes",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer lr.Body.Close()
	var lres map[string]interface{}
	require.NoError(t, json.NewDecoder(lr.Body).Decode(&lres))
	require.Nil(t, lres["error"])
	text := lres["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	assert.Contains(t, text, fmt.Sprintf("note-%d", workID))
	assert.NotContains(t, text, fmt.Sprintf("note-%d", privateID), "private note must be hidden from scoped token")

	// list_notes inside Private should be denied outright.
	dr := m.post(t, rpc(2, "tools/call", map[string]interface{}{
		"name":      "list_notes",
		"arguments": map[string]interface{}{"folder_id": privateID},
	}), sessionID)
	defer dr.Body.Close()
	var dres map[string]interface{}
	require.NoError(t, json.NewDecoder(dr.Body).Decode(&dres))
	assert.NotNil(t, dres["error"])
}

func TestMCP_Scoped_AncestorGrantCascades(t *testing.T) {
	tc := newTestClient(t)
	tc.registerAndLogin(t)

	// Build Work/Projects and grant write on Work only.
	workID := createFolderHelper(t, tc, "Work")
	projResp := tc.do(t, http.MethodPost, "/api/v1/folders", map[string]interface{}{
		"name":      "Projects",
		"parent_id": workID,
	})
	require.Equal(t, http.StatusCreated, projResp.StatusCode)
	var proj map[string]interface{}
	require.NoError(t, json.NewDecoder(projResp.Body).Decode(&proj))
	projResp.Body.Close()
	projID := int64(proj["id"].(float64))

	resp := tc.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{
		"name":  "cascade",
		"scope": "readwrite",
	})
	defer resp.Body.Close()
	var created map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	tokenID := int64(created["id"].(float64))
	raw := created["token"].(string)

	permResp := tc.do(t, http.MethodPut, fmt.Sprintf("/api/v1/account/tokens/%d/permissions", tokenID), map[string]interface{}{
		"folder_permissions": []map[string]interface{}{
			{"folder_id": workID, "permission": "write"},
		},
	})
	defer permResp.Body.Close()
	require.Equal(t, http.StatusOK, permResp.StatusCode)

	m := &mcpClient{testClient: tc, bearerToken: raw}
	sessionID := m.initialize(t)

	// Writing inside the nested Projects folder must succeed via ancestor grant.
	cr := m.post(t, rpc(1, "tools/call", map[string]interface{}{
		"name": "create_note",
		"arguments": map[string]interface{}{
			"title":     "nested",
			"folder_id": projID,
		},
	}), sessionID)
	defer cr.Body.Close()
	var cres map[string]interface{}
	require.NoError(t, json.NewDecoder(cr.Body).Decode(&cres))
	assert.Nil(t, cres["error"], "ancestor write grant should cascade to children")
}

// TestMCP_Scoped_ScopeDowngradeTakesEffect verifies that flipping a token from
// readwrite to read via the update endpoint immediately blocks write tools.
func TestMCP_Scoped_ScopeDowngradeTakesEffect(t *testing.T) {
	tc := newTestClient(t)
	tc.registerAndLogin(t)
	workID := createFolderHelper(t, tc, "Work")

	// Create a readwrite token.
	resp := tc.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{
		"name":  "downgrade",
		"scope": "readwrite",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var created map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	tokenID := int64(created["id"].(float64))
	raw := created["token"].(string)

	m := &mcpClient{testClient: tc, bearerToken: raw}
	sessionID := m.initialize(t)

	// First create should succeed (readwrite).
	cr1 := m.post(t, rpc(1, "tools/call", map[string]interface{}{
		"name": "create_note",
		"arguments": map[string]interface{}{
			"title":     "before-downgrade",
			"folder_id": workID,
		},
	}), sessionID)
	defer cr1.Body.Close()
	var cres1 map[string]interface{}
	require.NoError(t, json.NewDecoder(cr1.Body).Decode(&cres1))
	require.Nil(t, cres1["error"], "readwrite token should create notes")

	// Downgrade scope to read via the permissions endpoint.
	permResp := tc.do(t, http.MethodPut, fmt.Sprintf("/api/v1/account/tokens/%d/permissions", tokenID), map[string]interface{}{
		"scope":              "read",
		"folder_permissions": []map[string]interface{}{},
	})
	defer permResp.Body.Close()
	require.Equal(t, http.StatusOK, permResp.StatusCode)

	// Second create must be rejected — the bearer middleware re-reads scope
	// on every request, so the downgrade is live immediately.
	cr2 := m.post(t, rpc(2, "tools/call", map[string]interface{}{
		"name": "create_note",
		"arguments": map[string]interface{}{
			"title":     "after-downgrade",
			"folder_id": workID,
		},
	}), sessionID)
	defer cr2.Body.Close()
	var cres2 map[string]interface{}
	require.NoError(t, json.NewDecoder(cr2.Body).Decode(&cres2))
	errObj, _ := cres2["error"].(map[string]interface{})
	require.NotNil(t, errObj, "downgraded token must be rejected on write")
	assert.Contains(t, errObj["message"].(string), "read-only")
}

// TestMCP_Scoped_FindFolders_RespectsAncestorGrant is a regression for a
// bug shipped in v1.5.11.1 and earlier: find_folders returned zero results
// when the matching folder inherited access from a granted ancestor two or
// more levels up. filterFolderTree was feeding the search subset into
// TokenAuthz.FilterReadableFolderIDs, whose parent-chain map was built from
// that same subset — so the walk from (deeply nested match) up to (granted
// ancestor outside the subset) broke on the first missing parent.
//
// Repro: Work/Projects/Q3, grant on Work. find_folders("Q3") returned [].
// Expected: [Q3].
func TestMCP_Scoped_FindFolders_RespectsAncestorGrant(t *testing.T) {
	tc := newTestClient(t)
	tc.registerAndLogin(t)

	// Build Work > Projects > Q3 — the matching folder is two levels deep.
	workID := createFolderHelper(t, tc, "Work")
	projResp := tc.do(t, http.MethodPost, "/api/v1/folders", map[string]interface{}{
		"name":      "Projects",
		"parent_id": workID,
	})
	require.Equal(t, http.StatusCreated, projResp.StatusCode)
	var proj map[string]interface{}
	require.NoError(t, json.NewDecoder(projResp.Body).Decode(&proj))
	projResp.Body.Close()
	projID := int64(proj["id"].(float64))

	q3Resp := tc.do(t, http.MethodPost, "/api/v1/folders", map[string]interface{}{
		"name":      "Q3",
		"parent_id": projID,
	})
	require.Equal(t, http.StatusCreated, q3Resp.StatusCode)
	q3Resp.Body.Close()

	// Token scoped to Work (read). Q3 must be reachable via ancestor cascade.
	resp := tc.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{
		"name":  "ancestor-find",
		"scope": "readwrite",
	})
	defer resp.Body.Close()
	var created map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	tokenID := int64(created["id"].(float64))
	raw := created["token"].(string)

	permResp := tc.do(t, http.MethodPut, fmt.Sprintf("/api/v1/account/tokens/%d/permissions", tokenID), map[string]interface{}{
		"folder_permissions": []map[string]interface{}{
			{"folder_id": workID, "permission": "read"},
		},
	})
	defer permResp.Body.Close()
	require.Equal(t, http.StatusOK, permResp.StatusCode)

	m := &mcpClient{testClient: tc, bearerToken: raw}
	sessionID := m.initialize(t)

	// find_folders "Q3" — match is two levels below the granted folder.
	fr := m.post(t, rpc(1, "tools/call", map[string]interface{}{
		"name":      "find_folders",
		"arguments": map[string]interface{}{"query": "Q3"},
	}), sessionID)
	defer fr.Body.Close()
	var fres map[string]interface{}
	require.NoError(t, json.NewDecoder(fr.Body).Decode(&fres))
	require.Nil(t, fres["error"])
	text := fres["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var found []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &found))
	assert.Lenf(t, found, 1, "find_folders should return Q3 via ancestor cascade; got %s", text)
	if len(found) == 1 {
		assert.Equal(t, "Q3", found[0]["name"])
	}

	// Same for "Projects" — one level below the grant. Was already passing
	// because the subset happened to contain Projects' direct parent, but
	// guard it so future refactors can't regress.
	pr := m.post(t, rpc(2, "tools/call", map[string]interface{}{
		"name":      "find_folders",
		"arguments": map[string]interface{}{"query": "Projects"},
	}), sessionID)
	defer pr.Body.Close()
	var pres map[string]interface{}
	require.NoError(t, json.NewDecoder(pr.Body).Decode(&pres))
	require.Nil(t, pres["error"])
	pText := pres["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var pFound []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(pText), &pFound))
	assert.Lenf(t, pFound, 1, "find_folders(Projects) should return it via Work grant; got %s", pText)
}

// TestMCP_Scoped_FindFolders_DoesNotLeakUngranted guards the opposite
// direction — a folder that does NOT match any grant must stay hidden.
func TestMCP_Scoped_FindFolders_DoesNotLeakUngranted(t *testing.T) {
	tc := newTestClient(t)
	tc.registerAndLogin(t)

	workID := createFolderHelper(t, tc, "Work")
	_ = createFolderHelper(t, tc, "Personal")

	resp := tc.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{
		"name":  "work-only",
		"scope": "readwrite",
	})
	defer resp.Body.Close()
	var created map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	tokenID := int64(created["id"].(float64))
	raw := created["token"].(string)

	permResp := tc.do(t, http.MethodPut, fmt.Sprintf("/api/v1/account/tokens/%d/permissions", tokenID), map[string]interface{}{
		"folder_permissions": []map[string]interface{}{
			{"folder_id": workID, "permission": "read"},
		},
	})
	defer permResp.Body.Close()
	require.Equal(t, http.StatusOK, permResp.StatusCode)

	m := &mcpClient{testClient: tc, bearerToken: raw}
	sessionID := m.initialize(t)

	fr := m.post(t, rpc(1, "tools/call", map[string]interface{}{
		"name":      "find_folders",
		"arguments": map[string]interface{}{"query": "Personal"},
	}), sessionID)
	defer fr.Body.Close()
	var fres map[string]interface{}
	require.NoError(t, json.NewDecoder(fr.Body).Decode(&fres))
	require.Nil(t, fres["error"])
	text := fres["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var found []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &found))
	assert.Emptyf(t, found, "Personal is ungranted, find_folders must not leak it; got %s", text)
}

// TestMCP_Scoped_ListFolders_NestedGrantShowsDescendants verifies that
// list_folders still returns the full readable subtree (including the
// granted folder + all descendants) after the filter refactor.
func TestMCP_Scoped_ListFolders_NestedGrantShowsDescendants(t *testing.T) {
	tc := newTestClient(t)
	tc.registerAndLogin(t)

	workID := createFolderHelper(t, tc, "Work")
	projResp := tc.do(t, http.MethodPost, "/api/v1/folders", map[string]interface{}{
		"name":      "Projects",
		"parent_id": workID,
	})
	require.Equal(t, http.StatusCreated, projResp.StatusCode)
	var proj map[string]interface{}
	require.NoError(t, json.NewDecoder(projResp.Body).Decode(&proj))
	projResp.Body.Close()
	projID := int64(proj["id"].(float64))
	q3Resp := tc.do(t, http.MethodPost, "/api/v1/folders", map[string]interface{}{
		"name":      "Q3",
		"parent_id": projID,
	})
	require.Equal(t, http.StatusCreated, q3Resp.StatusCode)
	q3Resp.Body.Close()
	_ = createFolderHelper(t, tc, "Personal")

	resp := tc.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{
		"name":  "work-tree",
		"scope": "readwrite",
	})
	defer resp.Body.Close()
	var created map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	tokenID := int64(created["id"].(float64))
	raw := created["token"].(string)

	permResp := tc.do(t, http.MethodPut, fmt.Sprintf("/api/v1/account/tokens/%d/permissions", tokenID), map[string]interface{}{
		"folder_permissions": []map[string]interface{}{
			{"folder_id": workID, "permission": "read"},
		},
	})
	defer permResp.Body.Close()
	require.Equal(t, http.StatusOK, permResp.StatusCode)

	m := &mcpClient{testClient: tc, bearerToken: raw}
	sessionID := m.initialize(t)

	lr := m.post(t, rpc(1, "tools/call", map[string]interface{}{
		"name":      "list_folders",
		"arguments": map[string]interface{}{},
	}), sessionID)
	defer lr.Body.Close()
	var lres map[string]interface{}
	require.NoError(t, json.NewDecoder(lr.Body).Decode(&lres))
	require.Nil(t, lres["error"])
	text := lres["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var folders []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &folders))
	names := make([]string, 0, len(folders))
	for _, f := range folders {
		names = append(names, f["name"].(string))
	}
	assert.Contains(t, names, "Work", "granted folder must be visible")
	assert.Contains(t, names, "Projects", "descendant must be visible via cascade")
	assert.Contains(t, names, "Q3", "grand-descendant must be visible via cascade")
	assert.NotContains(t, names, "Personal", "ungranted sibling must stay hidden")
}
