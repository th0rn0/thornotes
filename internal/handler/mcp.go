package handler

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/notes"
)

const mcpProtocolVersion = "2025-03-26"

// mcpSessions tracks live session IDs in memory.
// A session is created on initialize and must be included in subsequent requests.
type mcpSessions struct {
	mu  sync.RWMutex
	ids map[string]struct{}
}

func (s *mcpSessions) create() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := fmt.Sprintf("%x", b)
	s.mu.Lock()
	s.ids[id] = struct{}{}
	s.mu.Unlock()
	return id
}

func (s *mcpSessions) valid(id string) bool {
	s.mu.RLock()
	_, ok := s.ids[id]
	s.mu.RUnlock()
	return ok
}

func (s *mcpSessions) remove(id string) {
	s.mu.Lock()
	delete(s.ids, id)
	s.mu.Unlock()
}

// MCPHandler implements the Model Context Protocol over Streamable HTTP transport.
// Spec: https://spec.modelcontextprotocol.io/specification/2025-03-26/basic/transports/#streamable-http
//
// Three endpoints:
//
//	POST   /mcp  — client→server messages (requests + notifications)
//	GET    /mcp  — server→client SSE stream (server-initiated messages)
//	DELETE /mcp  — terminate a session
type MCPHandler struct {
	notes    *notes.Service
	sessions mcpSessions
}

func NewMCPHandler(notes *notes.Service) *MCPHandler {
	return &MCPHandler{
		notes:    notes,
		sessions: mcpSessions{ids: make(map[string]struct{})},
	}
}

// ── JSON-RPC 2.0 types ────────────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

// isNotification returns true when the JSON-RPC message has no id field (or
// id is null), meaning it is a notification and requires no response.
func isNotification(id json.RawMessage) bool {
	return len(id) == 0 || string(id) == "null"
}

func rpcOK(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func rpcErr(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// ── MCP protocol types ────────────────────────────────────────────────────────

type mcpServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type mcpCapabilities struct {
	Tools     map[string]any `json:"tools,omitempty"`
	Resources map[string]any `json:"resources,omitempty"`
}

type mcpInitializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	ServerInfo      mcpServerInfo   `json:"serverInfo"`
	Capabilities    mcpCapabilities `json:"capabilities"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type mcpTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

// HandlePOST handles POST /mcp — all client→server messages.
// Accepts a single JSON-RPC object or a JSON array of batched messages.
// Notifications (no id field) are handled silently and return 202.
func (h *MCPHandler) HandlePOST(c *gin.Context) {
	user := auth.UserFromContext(c.Request.Context())
	sessionID := c.GetHeader("Mcp-Session-Id")

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20) // 4 MiB
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.Status(http.StatusRequestEntityTooLarge)
		return
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		c.Header("Content-Type", "application/json")
		writeRPCJSON(c.Writer, rpcErr(nil, rpcParseError, "empty body"))
		return
	}

	// Batch request — JSON array.
	if trimmed[0] == '[' {
		h.handleBatch(c, trimmed, sessionID, user.ID)
		return
	}

	// Single request.
	var req rpcRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		c.Header("Content-Type", "application/json")
		writeRPCJSON(c.Writer, rpcErr(nil, rpcParseError, "parse error"))
		return
	}
	if req.JSONRPC != "2.0" {
		c.Header("Content-Type", "application/json")
		writeRPCJSON(c.Writer, rpcErr(req.ID, rpcInvalidRequest, "jsonrpc must be '2.0'"))
		return
	}

	// Notifications require no response.
	if isNotification(req.ID) {
		c.Status(http.StatusAccepted)
		return
	}

	// initialize creates a new session.
	if req.Method == "initialize" {
		newID := h.sessions.create()
		c.Header("Mcp-Session-Id", newID)
		c.Header("Content-Type", "application/json")
		writeRPCJSON(c.Writer, h.dispatch(c.Request, req, user.ID))
		return
	}

	// All other requests: validate session ID if provided.
	if sessionID != "" && !h.sessions.valid(sessionID) {
		c.Status(http.StatusNotFound)
		return
	}

	c.Header("Content-Type", "application/json")
	writeRPCJSON(c.Writer, h.dispatch(c.Request, req, user.ID))
}

// HandleGET handles GET /mcp — opens a server-sent event stream.
// thornotes has no server-initiated messages, so this holds the connection
// open with periodic keepalive comments until the client disconnects.
func (h *MCPHandler) HandleGET(c *gin.Context) {
	sessionID := c.GetHeader("Mcp-Session-Id")
	if sessionID != "" && !h.sessions.valid(sessionID) {
		c.Status(http.StatusNotFound)
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	ctx := c.Request.Context()
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	// Write an initial SSE comment to flush the response headers to the client
	// immediately, before blocking on the ticker.
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	c.Stream(func(w io.Writer) bool {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			fmt.Fprint(w, ": keepalive\n\n")
			return true
		}
	})
}

// HandleDELETE handles DELETE /mcp — terminates a session.
func (h *MCPHandler) HandleDELETE(c *gin.Context) {
	sessionID := c.GetHeader("Mcp-Session-Id")
	if sessionID == "" {
		c.Status(http.StatusBadRequest)
		return
	}
	if !h.sessions.valid(sessionID) {
		c.Status(http.StatusNotFound)
		return
	}
	h.sessions.remove(sessionID)
	c.Status(http.StatusOK)
}

// handleBatch processes a JSON array of JSON-RPC messages.
// Notifications are handled silently. If all messages are notifications, returns 202.
func (h *MCPHandler) handleBatch(c *gin.Context, body []byte, sessionID string, userID int64) {
	var reqs []rpcRequest
	if err := json.Unmarshal(body, &reqs); err != nil {
		c.Header("Content-Type", "application/json")
		writeRPCJSON(c.Writer, rpcErr(nil, rpcParseError, "parse error"))
		return
	}
	if len(reqs) == 0 {
		c.Header("Content-Type", "application/json")
		writeRPCJSON(c.Writer, rpcErr(nil, rpcInvalidRequest, "empty batch"))
		return
	}

	var responses []rpcResponse
	for _, req := range reqs {
		if req.JSONRPC != "2.0" || isNotification(req.ID) {
			continue
		}
		if sessionID != "" && !h.sessions.valid(sessionID) {
			responses = append(responses, rpcErr(req.ID, -32001, "session not found"))
			continue
		}
		responses = append(responses, h.dispatch(c.Request, req, userID))
	}

	if len(responses) == 0 {
		c.Status(http.StatusAccepted)
		return
	}
	c.Header("Content-Type", "application/json")
	_ = json.NewEncoder(c.Writer).Encode(responses)
}

// ── Dispatcher ────────────────────────────────────────────────────────────────

// dispatch routes a JSON-RPC request to the correct MCP method handler
// and returns the response object.
func (h *MCPHandler) dispatch(r *http.Request, req rpcRequest, userID int64) rpcResponse {
	switch req.Method {
	case "initialize":
		return h.handleInitialize(req)
	case "tools/list":
		return h.handleToolsList(req)
	case "tools/call":
		return h.handleToolsCall(r, req, userID)
	case "resources/list":
		return h.handleResourcesList(r, req, userID)
	case "resources/read":
		return h.handleResourcesRead(r, req, userID)
	default:
		return rpcErr(req.ID, rpcMethodNotFound, "method not found: "+req.Method)
	}
}

// ── MCP method handlers ───────────────────────────────────────────────────────

func (h *MCPHandler) handleInitialize(req rpcRequest) rpcResponse {
	return rpcOK(req.ID, mcpInitializeResult{
		ProtocolVersion: mcpProtocolVersion,
		ServerInfo:      mcpServerInfo{Name: "thornotes", Version: "1.0"},
		Capabilities: mcpCapabilities{
			Tools:     map[string]any{"listChanged": false},
			Resources: map[string]any{"listChanged": false},
		},
	})
}

func (h *MCPHandler) handleToolsList(req rpcRequest) rpcResponse {
	tools := []mcpTool{
		{
			Name: "list_notes",
			Description: "List note metadata (id, title, slug, tags, folder_id, updated_at) across all folders. " +
				"Omit folder_id to get all notes. Use get_note to fetch the full markdown content of a specific note.",
			InputSchema: jsonSchema(map[string]any{
				"folder_id": prop("integer", "Filter to a specific folder. Omit to list all notes across all folders."),
			}, nil),
		},
		{
			Name: "get_note",
			Description: "Fetch the complete markdown content of a note by ID. " +
				"Returns title, content, tags, folder_id, and metadata. " +
				"Use list_notes or search_notes to find note IDs.",
			InputSchema: jsonSchema(map[string]any{
				"id": prop("integer", "Note ID (from list_notes or search_notes results)"),
			}, []string{"id"}),
		},
		{
			Name: "search_notes",
			Description: "Full-text search across all notes. Returns id, title, snippet, and tags. " +
				"Use get_note to read full content. Optionally filter by one or more tags.",
			InputSchema: jsonSchema(map[string]any{
				"query": prop("string", "Full-text search query"),
				"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Filter results to notes with all of these tags"},
			}, []string{"query"}),
		},
		{
			Name: "list_folders",
			Description: "Get the full folder hierarchy (id, parent_id, name, note_count). " +
				"Use folder IDs with list_notes or create_note to scope to a specific folder.",
			InputSchema: jsonSchema(nil, nil),
		},
		{
			Name:        "find_folders",
			Description: "Find folders by name (case-insensitive substring match). Returns matching folders with id, parent_id, name, and note_count. Useful for locating a folder before scoping list_notes or create_note to it.",
			InputSchema: jsonSchema(map[string]any{
				"query": prop("string", "Substring to search for in folder names"),
			}, []string{"query"}),
		},
		{
			Name:        "find_notes_by_tag",
			Description: "List all notes that have every specified tag (AND semantics). Returns id, title, slug, tags, folder_id, and updated_at. Use get_note for full content. Unlike search_notes, no full-text query is required.",
			InputSchema: jsonSchema(map[string]any{
				"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "One or more tags — only notes with ALL of these tags are returned"},
			}, []string{"tags"}),
		},
		{
			Name:        "list_tags",
			Description: "Return all unique tags in use across all notes, sorted alphabetically. Useful for discovering available tags before calling find_notes_by_tag.",
			InputSchema: jsonSchema(nil, nil),
		},
		{
			Name: "create_note",
			Description: "Create a new markdown note. Returns the created note including its ID.",
			InputSchema: jsonSchema(map[string]any{
				"title":     prop("string", "Note title (1–500 characters)"),
				"content":   prop("string", "Initial markdown content (optional)"),
				"folder_id": prop("integer", "Folder to create the note in (optional, defaults to root)"),
				"tags":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags to apply to the note"},
			}, []string{"title"}),
		},
		{
			Name: "update_note",
			Description: "Replace the markdown content of an existing note. Handles optimistic concurrency automatically.",
			InputSchema: jsonSchema(map[string]any{
				"id":      prop("integer", "Note ID"),
				"content": prop("string", "New full markdown content (replaces existing content)"),
			}, []string{"id", "content"}),
		},
	}
	return rpcOK(req.ID, map[string]any{"tools": tools})
}

func (h *MCPHandler) handleToolsCall(r *http.Request, req rpcRequest, userID int64) rpcResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcErr(req.ID, rpcInvalidParams, "invalid params")
	}

	ctx := r.Context()

	switch params.Name {
	case "search_notes":
		var args struct {
			Query string   `json:"query"`
			Tags  []string `json:"tags"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.Query == "" {
			return rpcErr(req.ID, rpcInvalidParams, "query is required")
		}
		results, err := h.notes.Search(ctx, userID, args.Query, args.Tags)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "search failed")
		}
		text, _ := json.Marshal(results)
		return rpcOK(req.ID, toolResult(string(text)))

	case "get_note":
		var args struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.ID == 0 {
			return rpcErr(req.ID, rpcInvalidParams, "id is required")
		}
		note, err := h.notes.GetNote(ctx, userID, args.ID)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, errorString(err))
		}
		text, _ := json.Marshal(note)
		return rpcOK(req.ID, toolResult(string(text)))

	case "list_notes":
		var args struct {
			FolderID *int64 `json:"folder_id"`
		}
		_ = json.Unmarshal(params.Arguments, &args)
		var (
			items []*model.NoteListItem
			err   error
		)
		if args.FolderID != nil {
			items, err = h.notes.ListNotes(ctx, userID, args.FolderID)
		} else {
			items, err = h.notes.ListAllNotes(ctx, userID)
		}
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "list failed")
		}
		text, _ := json.Marshal(items)
		return rpcOK(req.ID, toolResult(string(text)))

	case "create_note":
		var args struct {
			Title    string   `json:"title"`
			Content  string   `json:"content"`
			FolderID *int64   `json:"folder_id"`
			Tags     []string `json:"tags"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.Title == "" {
			return rpcErr(req.ID, rpcInvalidParams, "title is required")
		}
		if args.Tags == nil {
			args.Tags = []string{}
		}
		note, err := h.notes.CreateNote(ctx, userID, args.FolderID, args.Title, args.Tags)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, errorString(err))
		}
		if args.Content != "" {
			newHash, err := h.notes.UpdateNoteContent(ctx, userID, note.ID, args.Content, note.ContentHash)
			if err != nil {
				return rpcErr(req.ID, rpcInternalError, errorString(err))
			}
			note.Content = args.Content
			note.ContentHash = newHash
		}
		text, _ := json.Marshal(note)
		return rpcOK(req.ID, toolResult(string(text)))

	case "update_note":
		var args struct {
			ID      int64  `json:"id"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.ID == 0 || args.Content == "" {
			return rpcErr(req.ID, rpcInvalidParams, "id and content are required")
		}
		note, err := h.notes.GetNote(ctx, userID, args.ID)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, errorString(err))
		}
		newHash, err := h.notes.UpdateNoteContent(ctx, userID, args.ID, args.Content, note.ContentHash)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, errorString(err))
		}
		note.Content = args.Content
		note.ContentHash = newHash
		text, _ := json.Marshal(note)
		return rpcOK(req.ID, toolResult(string(text)))

	case "list_folders":
		folders, err := h.notes.FolderTree(ctx, userID)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "list failed")
		}
		text, _ := json.Marshal(folders)
		return rpcOK(req.ID, toolResult(string(text)))

	case "find_folders":
		var args struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.Query == "" {
			return rpcErr(req.ID, rpcInvalidParams, "query is required")
		}
		folders, err := h.notes.FindFoldersByName(ctx, userID, args.Query)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "search failed")
		}
		text, _ := json.Marshal(folders)
		return rpcOK(req.ID, toolResult(string(text)))

	case "find_notes_by_tag":
		var args struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || len(args.Tags) == 0 {
			return rpcErr(req.ID, rpcInvalidParams, "tags must be a non-empty array")
		}
		items, err := h.notes.FindNotesByTag(ctx, userID, args.Tags)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "search failed")
		}
		text, _ := json.Marshal(items)
		return rpcOK(req.ID, toolResult(string(text)))

	case "list_tags":
		tags, err := h.notes.ListAllTags(ctx, userID)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "list failed")
		}
		text, _ := json.Marshal(tags)
		return rpcOK(req.ID, toolResult(string(text)))

	default:
		return rpcErr(req.ID, rpcMethodNotFound, "unknown tool: "+params.Name)
	}
}

func (h *MCPHandler) handleResourcesList(r *http.Request, req rpcRequest, userID int64) rpcResponse {
	items, err := h.notes.ListAllNotes(r.Context(), userID)
	if err != nil {
		return rpcErr(req.ID, rpcInternalError, "list notes failed")
	}
	resources := make([]mcpResource, 0, len(items))
	for _, item := range items {
		desc := ""
		if len(item.Tags) > 0 {
			tagsJSON, _ := json.Marshal(item.Tags)
			desc = "tags: " + string(tagsJSON)
		}
		resources = append(resources, mcpResource{
			URI:         "note://" + itoa(item.ID),
			Name:        item.Title,
			Description: desc,
			MimeType:    "text/markdown",
		})
	}
	return rpcOK(req.ID, map[string]any{"resources": resources})
}

func (h *MCPHandler) handleResourcesRead(r *http.Request, req rpcRequest, userID int64) rpcResponse {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcErr(req.ID, rpcInvalidParams, "invalid params")
	}

	const prefix = "note://"
	if len(params.URI) <= len(prefix) || params.URI[:len(prefix)] != prefix {
		return rpcErr(req.ID, rpcInvalidParams, "invalid resource URI")
	}
	idStr := params.URI[len(prefix):]
	var id int64
	for _, c := range idStr {
		if c < '0' || c > '9' {
			return rpcErr(req.ID, rpcInvalidParams, "invalid note id in URI")
		}
		id = id*10 + int64(c-'0')
	}

	note, err := h.notes.GetNote(r.Context(), userID, id)
	if err != nil {
		return rpcErr(req.ID, rpcInternalError, errorString(err))
	}
	return rpcOK(req.ID, map[string]any{
		"contents": []mcpTextContent{
			{Type: "text", Text: note.Content},
		},
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeRPCJSON(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func toolResult(text string) map[string]any {
	return map[string]any{
		"content": []mcpTextContent{{Type: "text", Text: text}},
	}
}

func jsonSchema(properties map[string]any, required []string) map[string]any {
	s := map[string]any{
		"type": "object",
	}
	if len(properties) > 0 {
		s["properties"] = properties
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func prop(typ, description string) map[string]any {
	return map[string]any{"type": typ, "description": description}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

func errorString(err error) string {
	var appErr *apperror.AppError
	if isAppErr(err, &appErr) {
		return appErr.Message
	}
	return "internal error"
}

func isAppErr(err error, target **apperror.AppError) bool {
	if err == nil {
		return false
	}
	if ae, ok := err.(*apperror.AppError); ok {
		*target = ae
		return true
	}
	return false
}
