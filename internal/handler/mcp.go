package handler

import (
	"encoding/json"
	"net/http"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/auth"
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

func (h *MCPHandler) Handle(w http.ResponseWriter, r *http.Request) {
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
			Name:        "search_notes",
			Description: "Search notes by keyword query.",
			InputSchema: jsonSchema(map[string]any{
				"query": prop("string", "Search query string"),
			}, []string{"query"}),
		},
		{
			Name:        "get_note",
			Description: "Get the full content of a note by its ID.",
			InputSchema: jsonSchema(map[string]any{
				"id": prop("integer", "Note ID"),
			}, []string{"id"}),
		},
		{
			Name:        "list_notes",
			Description: "List all notes (optionally filtered by folder ID).",
			InputSchema: jsonSchema(map[string]any{
				"folder_id": prop("integer", "Optional folder ID to filter by"),
			}, nil),
		},
		{
			Name:        "create_note",
			Description: "Create a new note.",
			InputSchema: jsonSchema(map[string]any{
				"title":     prop("string", "Note title"),
				"content":   prop("string", "Initial note content (markdown)"),
				"folder_id": prop("integer", "Optional folder ID"),
			}, []string{"title"}),
		},
		{
			Name:        "update_note",
			Description: "Update the content of an existing note.",
			InputSchema: jsonSchema(map[string]any{
				"id":      prop("integer", "Note ID"),
				"content": prop("string", "New content (markdown)"),
			}, []string{"id", "content"}),
		},
		{
			Name:        "list_folders",
			Description: "List all folders.",
			InputSchema: jsonSchema(nil, nil),
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
			Query string `json:"query"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.Query == "" {
			writeRPCError(w, req.ID, rpcInvalidParams, "query is required")
			return
		}
		results, err := h.notes.Search(ctx, userID, args.Query, nil)
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
		items, err := h.notes.ListNotes(ctx, userID, args.FolderID)
		if err != nil {
			writeRPCError(w, req.ID, rpcInternalError, "list failed")
			return
		}
		text, _ := json.Marshal(items)
		writeRPCResult(w, req.ID, toolResult(string(text)))

	case "create_note":
		var args struct {
			Title    string `json:"title"`
			Content  string `json:"content"`
			FolderID *int64 `json:"folder_id"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.Title == "" {
			writeRPCError(w, req.ID, rpcInvalidParams, "title is required")
			return
		}
		note, err := h.notes.CreateNote(ctx, userID, args.FolderID, args.Title, []string{})
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
	items, err := h.notes.ListNotes(r.Context(), userID, nil)
	if err != nil {
		writeRPCError(w, req.ID, rpcInternalError, "list notes failed")
		return
	}
	resources := make([]mcpResource, 0, len(items))
	for _, item := range items {
		resources = append(resources, mcpResource{
			URI:      "note://" + itoa(item.ID),
			Name:     item.Title,
			MimeType: "text/markdown",
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
