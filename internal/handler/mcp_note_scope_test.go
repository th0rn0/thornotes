package handler_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpNotFoundCode is the JSON-RPC code mcp.go uses (rpcErrNotFound) for both
// "no such note" and "note outside this token's folder scope". Duplicated here
// because this is an external test package; the value is pinned by the tests
// below, so a change in mcp.go that breaks the convention fails them.
const mcpNotFoundCode = -32002

// mcpNotFoundMsg is the single message text paired with mcpNotFoundCode.
const mcpNotFoundMsg = "not found"

// noteScopeFixture is one user with two sibling folders — "Granted" (readable
// and writable by a folder-scoped token) and "Outside" (not) — plus one note
// in each, and an id that resolves to nothing.
type noteScopeFixture struct {
	client    *testClient
	mcp       *mcpClient
	sessionID string

	inScopeID    int64 // note inside the granted folder
	outOfScopeID int64 // note in the ungranted folder
	missingID    int64 // id that resolves to no note
}

func newNoteScopeFixture(t *testing.T) *noteScopeFixture {
	t.Helper()

	c := newTestClient(t)
	c.registerAndLogin(t)

	granted := createFolderHelper(t, c, "Granted")
	outside := createFolderHelper(t, c, "Outside")

	inScopeID := createNoteInFolder(t, c, "In Scope", &granted, "inside the granted folder")
	outOfScopeID := createNoteInFolder(t, c, "Out Of Scope", &outside, "outside the granted folder")

	// An id far above anything this fixture created. Proven not to resolve
	// through the REST surface — which already answers 404 for a missing note
	// (ticket 475) — so the MCP assertion below cannot be satisfied by an id
	// that secretly exists.
	missingID := outOfScopeID + 100000
	mr := c.do(t, http.MethodGet, fmt.Sprintf("/api/v1/notes/%d", missingID), nil)
	defer mr.Body.Close()
	require.Equal(t, http.StatusNotFound, mr.StatusCode, "fixture missing-id must not resolve")

	// A readwrite token scoped to the granted folder only: write tools reach
	// the note-scope check instead of being stopped by the token-wide
	// read-only gate, so every row below exercises the oracle itself.
	raw := createScopedToken(t, c, granted, "write")
	m := &mcpClient{testClient: c, bearerToken: raw}

	return &noteScopeFixture{
		client:       c,
		mcp:          m,
		sessionID:    m.initialize(t),
		inScopeID:    inScopeID,
		outOfScopeID: outOfScopeID,
		missingID:    missingID,
	}
}

// postRaw sends one MCP message and returns the raw response body, so a test
// can compare serialised bytes rather than decoded structures.
func (f *noteScopeFixture) postRaw(t *testing.T, body map[string]interface{}) []byte {
	t.Helper()
	resp := f.mcp.post(t, body, f.sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return raw
}

// rpcError decodes the JSON-RPC error object out of a raw response.
func rpcError(t *testing.T, raw []byte) (code int, message string) {
	t.Helper()
	var env struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(raw, &env), "response is not JSON: %s", raw)
	require.NotNil(t, env.Error, "expected a JSON-RPC error, got: %s", raw)
	return env.Error.Code, env.Error.Message
}

// noteIDCall is one MCP entry point that takes a note id in its arguments.
type noteIDCall struct {
	name string
	req  func(rpcID interface{}, noteID int64) map[string]interface{}
}

// noteIDCalls enumerates every MCP entry point that resolves a caller-supplied
// note id. Keep it in sync with mcp.go: the count assertion in the test below
// fails if a row disappears, so a future tool that takes a note id has to be
// added here (and therefore has to pass the same indistinguishability check).
func noteIDCalls() []noteIDCall {
	tool := func(name string, args func(int64) map[string]interface{}) func(interface{}, int64) map[string]interface{} {
		return func(rpcID interface{}, noteID int64) map[string]interface{} {
			return rpc(rpcID, "tools/call", map[string]interface{}{
				"name":      name,
				"arguments": args(noteID),
			})
		}
	}
	return []noteIDCall{
		{"tools/call get_note", tool("get_note", func(id int64) map[string]interface{} {
			return map[string]interface{}{"id": id}
		})},
		{"tools/call update_note", tool("update_note", func(id int64) map[string]interface{} {
			return map[string]interface{}{"id": id, "content": "replaced"}
		})},
		{"tools/call rename_note", tool("rename_note", func(id int64) map[string]interface{} {
			return map[string]interface{}{"id": id, "title": "Renamed"}
		})},
		{"tools/call move_note", tool("move_note", func(id int64) map[string]interface{} {
			return map[string]interface{}{"id": id}
		})},
		{"tools/call delete_note", tool("delete_note", func(id int64) map[string]interface{} {
			return map[string]interface{}{"id": id}
		})},
		{"tools/call get_links", tool("get_links", func(id int64) map[string]interface{} {
			return map[string]interface{}{"id": id}
		})},
		{"resources/read", func(rpcID interface{}, noteID int64) map[string]interface{} {
			return rpc(rpcID, "resources/read", map[string]interface{}{
				"uri": fmt.Sprintf("note://%d", noteID),
			})
		}},
	}
}

// TestMCP_NoteScopeOracle_OutOfScopeIsIndistinguishableFromMissing is the
// regression test for finding N11: a folder-scoped token must not be able to
// tell "that note exists but is outside your scope" from "that note does not
// exist". Both must produce the same bytes, id and message included, on every
// MCP entry point that takes a note id.
func TestMCP_NoteScopeOracle_OutOfScopeIsIndistinguishableFromMissing(t *testing.T) {
	f := newNoteScopeFixture(t)

	calls := noteIDCalls()
	require.Len(t, calls, 7, "a new MCP entry point taking a note id needs a row here")

	for _, call := range calls {
		call := call
		t.Run(call.name, func(t *testing.T) {
			// Same JSON-RPC id for both requests so the whole serialised
			// response — envelope, id and all — has to match byte for byte.
			const rpcID = 469
			outOfScope := f.postRaw(t, call.req(rpcID, f.outOfScopeID))
			missing := f.postRaw(t, call.req(rpcID, f.missingID))

			assert.Equal(t, string(missing), string(outOfScope),
				"out-of-scope and non-existent note ids must serialise identically")

			// ...and both must be the deliberate not-found answer, so two
			// identical failures of some other kind cannot satisfy the test.
			code, msg := rpcError(t, outOfScope)
			assert.Equal(t, mcpNotFoundCode, code, "out-of-scope note must answer rpcErrNotFound")
			assert.Equal(t, mcpNotFoundMsg, msg)
			code, msg = rpcError(t, missing)
			assert.Equal(t, mcpNotFoundCode, code, "missing note must answer rpcErrNotFound")
			assert.Equal(t, mcpNotFoundMsg, msg)
		})
	}
}

// TestMCP_NoteScopeOracle_PositiveControl proves the fix did not simply deny
// everything: a note inside the scope still returns its content, and the
// malformed-arguments carve-out (-32602) is still in place.
func TestMCP_NoteScopeOracle_PositiveControl(t *testing.T) {
	f := newNoteScopeFixture(t)

	// (a) A note inside the granted folder still returns its content.
	raw := f.postRaw(t, rpc(1, "tools/call", map[string]interface{}{
		"name":      "get_note",
		"arguments": map[string]interface{}{"id": f.inScopeID},
	}))
	var env struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error interface{} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(raw, &env))
	require.Nil(t, env.Error, "in-scope get_note must succeed: %s", raw)
	require.Len(t, env.Result.Content, 1)

	var note struct {
		ID      int64  `json:"id"`
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal([]byte(env.Result.Content[0].Text), &note))
	assert.Equal(t, f.inScopeID, note.ID)
	assert.Equal(t, "inside the granted folder", note.Content)

	// The link graph for an in-scope note is still served.
	linksRaw := f.postRaw(t, rpc(2, "tools/call", map[string]interface{}{
		"name":      "get_links",
		"arguments": map[string]interface{}{"id": f.inScopeID},
	}))
	var linksEnv struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error interface{} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(linksRaw, &linksEnv))
	require.Nil(t, linksEnv.Error, "in-scope get_links must succeed: %s", linksRaw)
	require.NotNil(t, linksEnv.Result)
	require.Len(t, linksEnv.Result.Content, 1)
	var graph struct {
		Outgoing  []interface{} `json:"outgoing"`
		Backlinks []interface{} `json:"backlinks"`
	}
	require.NoError(t, json.Unmarshal([]byte(linksEnv.Result.Content[0].Text), &graph))

	// (b) Malformed arguments are still JSON-RPC's business, not the store's:
	// a missing, zero or non-integer id answers -32602 on every tool that
	// takes one, and never the not-found answer.
	malformed := []struct {
		name string
		req  map[string]interface{}
	}{
		{"get_note zero id", rpc(10, "tools/call", map[string]interface{}{
			"name": "get_note", "arguments": map[string]interface{}{"id": 0}})},
		{"get_note missing id", rpc(11, "tools/call", map[string]interface{}{
			"name": "get_note", "arguments": map[string]interface{}{}})},
		{"get_note non-integer id", rpc(12, "tools/call", map[string]interface{}{
			"name": "get_note", "arguments": map[string]interface{}{"id": "not-an-id"}})},
		{"update_note zero id", rpc(13, "tools/call", map[string]interface{}{
			"name": "update_note", "arguments": map[string]interface{}{"id": 0, "content": "x"}})},
		{"rename_note missing id", rpc(14, "tools/call", map[string]interface{}{
			"name": "rename_note", "arguments": map[string]interface{}{"title": "x"}})},
		{"move_note non-integer id", rpc(15, "tools/call", map[string]interface{}{
			"name": "move_note", "arguments": map[string]interface{}{"id": "nope"}})},
		{"delete_note zero id", rpc(16, "tools/call", map[string]interface{}{
			"name": "delete_note", "arguments": map[string]interface{}{"id": 0}})},
		{"get_links missing id", rpc(17, "tools/call", map[string]interface{}{
			"name": "get_links", "arguments": map[string]interface{}{}})},
		{"resources/read non-numeric id", rpc(18, "resources/read", map[string]interface{}{
			"uri": "note://abc"})},
		{"resources/read empty id", rpc(19, "resources/read", map[string]interface{}{
			"uri": "note://"})},
	}
	for _, m := range malformed {
		m := m
		t.Run(m.name, func(t *testing.T) {
			code, msg := rpcError(t, f.postRaw(t, m.req))
			assert.Equal(t, -32602, code, "malformed id must stay rpcInvalidParams: %s", msg)
			assert.NotEqual(t, mcpNotFoundCode, code)
		})
	}
}

// TestMCP_NoteScopeOracle_ReadGrantWriteDenialStillNamesThePermission pins the
// deliberate exception: a token that can *read* a note already knows it
// exists, so refusing a write with "no write access" leaks nothing and is
// more useful than "not found". The oracle only had to close on notes the
// token cannot read at all.
func TestMCP_NoteScopeOracle_ReadGrantWriteDenialStillNamesThePermission(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	granted := createFolderHelper(t, c, "Granted")
	outside := createFolderHelper(t, c, "Outside")
	inScopeID := createNoteInFolder(t, c, "In Scope", &granted, "body")
	outOfScopeID := createNoteInFolder(t, c, "Out Of Scope", &outside, "body")

	raw := createScopedToken(t, c, granted, "read")
	f := &noteScopeFixture{
		client:       c,
		mcp:          &mcpClient{testClient: c, bearerToken: raw},
		inScopeID:    inScopeID,
		outOfScopeID: outOfScopeID,
		missingID:    outOfScopeID + 100000,
	}
	f.sessionID = f.mcp.initialize(t)

	// Readable note, write tool: forbidden, and it says which permission.
	code, msg := rpcError(t, f.postRaw(t, rpc(1, "tools/call", map[string]interface{}{
		"name":      "update_note",
		"arguments": map[string]interface{}{"id": inScopeID, "content": "x"},
	})))
	assert.Equal(t, -32001, code)
	assert.Contains(t, msg, "write access")

	// Unreadable note, write tool: indistinguishable from a missing note.
	outOfScope := f.postRaw(t, rpc(2, "tools/call", map[string]interface{}{
		"name":      "update_note",
		"arguments": map[string]interface{}{"id": outOfScopeID, "content": "x"},
	}))
	missing := f.postRaw(t, rpc(2, "tools/call", map[string]interface{}{
		"name":      "update_note",
		"arguments": map[string]interface{}{"id": f.missingID, "content": "x"},
	}))
	assert.Equal(t, string(missing), string(outOfScope))
	code, msg = rpcError(t, outOfScope)
	assert.Equal(t, mcpNotFoundCode, code)
	assert.Equal(t, mcpNotFoundMsg, msg)
}
