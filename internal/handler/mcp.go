package handler

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/notes"
)

// MCPHandler implements the Model Context Protocol over Streamable HTTP transport.
// Spec: https://spec.modelcontextprotocol.io/specification/basic/transports/
//
// All requests arrive as POST /mcp with a JSON-RPC 2.0 body.
// Responses are synchronous JSON-RPC 2.0 objects.
type MCPHandler struct {
	notes *notes.Service
}

func NewMCPHandler(notes *notes.Service) *MCPHandler {
	return &MCPHandler{notes: notes}
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

// ── Handler ───────────────────────────────────────────────────────────────────

// Handle is the gin handler — delegates to handle(w, r) to keep all internal
// MCP logic unchanged.
func (h *MCPHandler) Handle(c *gin.Context) {
	h.handle(c.Writer, c.Request)
}

func (h *MCPHandler) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, nil, rpcParseError, "parse error")
		return
	}
	if req.JSONRPC != "2.0" {
		writeRPCError(w, req.ID, rpcInvalidRequest, "jsonrpc must be '2.0'")
		return
	}

	user := auth.UserFromContext(r.Context())

	switch req.Method {
	case "initialize":
		h.handleInitialize(w, req)
	case "notifications/initialized":
		// Client acknowledgement — no response needed for notifications.
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		h.handleToolsList(w, req)
	case "tools/call":
		h.handleToolsCall(w, r, req, user.ID)
	case "resources/list":
		h.handleResourcesList(w, r, req, user.ID)
	case "resources/read":
		h.handleResourcesRead(w, r, req, user.ID)
	default:
		writeRPCError(w, req.ID, rpcMethodNotFound, "method not found: "+req.Method)
	}
}

func (h *MCPHandler) handleInitialize(w http.ResponseWriter, req rpcRequest) {
	writeRPCResult(w, req.ID, mcpInitializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      mcpServerInfo{Name: "thornotes", Version: "1.0"},
		Capabilities: mcpCapabilities{
			Tools:     map[string]any{"listChanged": false},
			Resources: map[string]any{"listChanged": false},
		},
	})
}

func (h *MCPHandler) handleToolsList(w http.ResponseWriter, req rpcRequest) {
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
	writeRPCResult(w, req.ID, map[string]any{"tools": tools})
}

func (h *MCPHandler) handleToolsCall(w http.ResponseWriter, r *http.Request, req rpcRequest, userID int64) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, rpcInvalidParams, "invalid params")
		return
	}

	ctx := r.Context()

	switch params.Name {
	case "search_notes":
		var args struct {
			Query string   `json:"query"`
			Tags  []string `json:"tags"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.Query == "" {
			writeRPCError(w, req.ID, rpcInvalidParams, "query is required")
			return
		}
		results, err := h.notes.Search(ctx, userID, args.Query, args.Tags)
		if err != nil {
			writeRPCError(w, req.ID, rpcInternalError, "search failed")
			return
		}
		text, _ := json.Marshal(results)
		writeRPCResult(w, req.ID, toolResult(string(text)))

	case "get_note":
		var args struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.ID == 0 {
			writeRPCError(w, req.ID, rpcInvalidParams, "id is required")
			return
		}
		note, err := h.notes.GetNote(ctx, userID, args.ID)
		if err != nil {
			writeRPCError(w, req.ID, rpcInternalError, errorString(err))
			return
		}
		text, _ := json.Marshal(note)
		writeRPCResult(w, req.ID, toolResult(string(text)))

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
			writeRPCError(w, req.ID, rpcInternalError, "list failed")
			return
		}
		text, _ := json.Marshal(items)
		writeRPCResult(w, req.ID, toolResult(string(text)))

	case "create_note":
		var args struct {
			Title    string   `json:"title"`
			Content  string   `json:"content"`
			FolderID *int64   `json:"folder_id"`
			Tags     []string `json:"tags"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.Title == "" {
			writeRPCError(w, req.ID, rpcInvalidParams, "title is required")
			return
		}
		if args.Tags == nil {
			args.Tags = []string{}
		}
		note, err := h.notes.CreateNote(ctx, userID, args.FolderID, args.Title, args.Tags)
		if err != nil {
			writeRPCError(w, req.ID, rpcInternalError, errorString(err))
			return
		}
		if args.Content != "" {
			newHash, err := h.notes.UpdateNoteContent(ctx, userID, note.ID, args.Content, note.ContentHash)
			if err != nil {
				writeRPCError(w, req.ID, rpcInternalError, errorString(err))
				return
			}
			note.Content = args.Content
			note.ContentHash = newHash
		}
		text, _ := json.Marshal(note)
		writeRPCResult(w, req.ID, toolResult(string(text)))

	case "update_note":
		var args struct {
			ID      int64  `json:"id"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.ID == 0 {
			writeRPCError(w, req.ID, rpcInvalidParams, "id and content are required")
			return
		}
		note, err := h.notes.GetNote(ctx, userID, args.ID)
		if err != nil {
			writeRPCError(w, req.ID, rpcInternalError, errorString(err))
			return
		}
		newHash, err := h.notes.UpdateNoteContent(ctx, userID, args.ID, args.Content, note.ContentHash)
		if err != nil {
			writeRPCError(w, req.ID, rpcInternalError, errorString(err))
			return
		}
		note.Content = args.Content
		note.ContentHash = newHash
		text, _ := json.Marshal(note)
		writeRPCResult(w, req.ID, toolResult(string(text)))

	case "list_folders":
		folders, err := h.notes.FolderTree(ctx, userID)
		if err != nil {
			writeRPCError(w, req.ID, rpcInternalError, "list failed")
			return
		}
		text, _ := json.Marshal(folders)
		writeRPCResult(w, req.ID, toolResult(string(text)))

	default:
		writeRPCError(w, req.ID, rpcMethodNotFound, "unknown tool: "+params.Name)
	}
}

func (h *MCPHandler) handleResourcesList(w http.ResponseWriter, r *http.Request, req rpcRequest, userID int64) {
	items, err := h.notes.ListAllNotes(r.Context(), userID)
	if err != nil {
		writeRPCError(w, req.ID, rpcInternalError, "list notes failed")
		return
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
	writeRPCResult(w, req.ID, map[string]any{"resources": resources})
}

func (h *MCPHandler) handleResourcesRead(w http.ResponseWriter, r *http.Request, req rpcRequest, userID int64) {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, rpcInvalidParams, "invalid params")
		return
	}

	// Parse note:// URI.
	const prefix = "note://"
	if len(params.URI) <= len(prefix) || params.URI[:len(prefix)] != prefix {
		writeRPCError(w, req.ID, rpcInvalidParams, "invalid resource URI")
		return
	}
	idStr := params.URI[len(prefix):]
	var id int64
	for _, c := range idStr {
		if c < '0' || c > '9' {
			writeRPCError(w, req.ID, rpcInvalidParams, "invalid note id in URI")
			return
		}
		id = id*10 + int64(c-'0')
	}

	note, err := h.notes.GetNote(r.Context(), userID, id)
	if err != nil {
		writeRPCError(w, req.ID, rpcInternalError, errorString(err))
		return
	}

	writeRPCResult(w, req.ID, map[string]any{
		"contents": []mcpTextContent{
			{Type: "text", Text: note.Content},
		},
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	_ = json.NewEncoder(w).Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	_ = json.NewEncoder(w).Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	})
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
