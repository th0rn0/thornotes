package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
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
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		// A timeout after headers are received is OK — we got what we need.
		// context.DeadlineExceeded with a non-nil resp means headers were received.
		if resp != nil {
			return resp
		}
		// If no resp, it's a 404 or similar pre-header error.
		// For 404, the server closes quickly so we won't hit the timeout.
		require.NoError(t, err)
	}
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
	resp := m.post(t, nil, "")
	// Override body with invalid JSON.
	req, _ := http.NewRequest(http.MethodPost, m.server.URL+"/mcp", bytes.NewReader([]byte("{bad json")))
	req.Header.Set("Authorization", "Bearer "+m.bearerToken)
	req.Header.Set("Content-Type", "application/json")
	resp.Body.Close()
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
	contentText := createResult["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var noteData map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(contentText), &noteData))
	noteID := int64(noteData["id"].(float64))

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
	contentText := createResult["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var noteData map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(contentText), &noteData))
	noteID := int64(noteData["id"].(float64))

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
	contentText := cResult["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	var noteData map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(contentText), &noteData))
	noteID := int64(noteData["id"].(float64))

	resp := m.post(t, rpc(18, "resources/read", map[string]interface{}{
		"uri": "note://" + string(rune('0'+noteID)),
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
