package handler

import (
	"bytes"
	"context"
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
	"github.com/th0rn0/thornotes/internal/repository"
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
		h.handleBatch(c, trimmed, sessionID, user)
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
		writeRPCJSON(c.Writer, h.dispatch(c.Request, req, user))
		return
	}

	// All other requests: validate session ID if provided.
	if sessionID != "" && !h.sessions.valid(sessionID) {
		c.Status(http.StatusNotFound)
		return
	}

	c.Header("Content-Type", "application/json")
	writeRPCJSON(c.Writer, h.dispatch(c.Request, req, user))
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
func (h *MCPHandler) handleBatch(c *gin.Context, body []byte, sessionID string, user *model.User) {
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
		responses = append(responses, h.dispatch(c.Request, req, user))
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
func (h *MCPHandler) dispatch(r *http.Request, req rpcRequest, user *model.User) rpcResponse {
	switch req.Method {
	case "initialize":
		return h.handleInitialize(req)
	case "tools/list":
		return h.handleToolsList(req)
	case "tools/call":
		return h.handleToolsCall(r, req, user)
	case "resources/list":
		return h.handleResourcesList(r, req, user.ID)
	case "resources/read":
		return h.handleResourcesRead(r, req, user.ID)
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
		// ── Read tools ────────────────────────────────────────────────────────────
		{
			Name: "list_notes",
			Description: "List note metadata for all notes or notes in a specific folder. " +
				"Returns an array of objects with fields: id (integer), title (string), slug (string), " +
				"tags (array of strings), folder_id (integer or null for root notes), updated_at (ISO 8601 timestamp). " +
				"Does NOT return note content — call get_note with the id to fetch the full markdown. " +
				"Typical workflow: call list_notes to discover what exists, then get_note for the ones you need.",
			InputSchema: jsonSchema(map[string]any{
				"folder_id": prop("integer", "Only return notes inside this folder. Omit or pass null to return all notes across every folder including root."),
			}, nil),
		},
		{
			Name: "get_note",
			Description: "Fetch the complete content and metadata of a single note. " +
				"Returns: id, title, slug, content (full markdown text), tags, folder_id (null = root), " +
				"content_hash (used internally for concurrency), created_at, updated_at. " +
				"Use search_notes or list_notes first to find the note id. " +
				"Example: get_note({id: 42}) returns the entire markdown document stored in the note.",
			InputSchema: jsonSchema(map[string]any{
				"id": prop("integer", "The note ID. Obtain this from list_notes, search_notes, or find_notes_by_tag."),
			}, []string{"id"}),
		},
		{
			Name: "search_notes",
			Description: "Full-text search across the title and content of all notes. " +
				"Returns an array with fields: id, title, snippet (a short excerpt around the match), tags, folder_id, updated_at. " +
				"Optionally narrow results to notes that carry ALL of the specified tags. " +
				"Call get_note with the returned id to read the full document. " +
				"Tip: use search_notes when you know keywords; use find_notes_by_tag when you know the tags.",
			InputSchema: jsonSchema(map[string]any{
				"query": prop("string", "The text to search for. Searches both note titles and body content."),
				"tags":  strArrayProp("Optionally restrict results to notes that have ALL of these tags (AND logic). Omit to search all notes."),
			}, []string{"query"}),
		},
		{
			Name: "list_folders",
			Description: "Return the complete folder tree. " +
				"Each folder has: id, parent_id (null for top-level folders), name, note_count (direct child notes only), disk_path (internal). " +
				"Use the id field when calling create_note, move_note, create_folder, or move_folder. " +
				"Use parent_id to reconstruct the hierarchy: folders with parent_id=null are top-level.",
			InputSchema: jsonSchema(nil, nil),
		},
		{
			Name: "find_folders",
			Description: "Case-insensitive substring search across folder names. " +
				"Returns matching folders with id, parent_id, name, and note_count. " +
				"Use this to locate a folder ID when you know part of its name but not the exact ID. " +
				"Example: find_folders({query: \"work\"}) returns 'Work', 'Work/Projects', 'Homework', etc.",
			InputSchema: jsonSchema(map[string]any{
				"query": prop("string", "Substring to search for in folder names. Case-insensitive."),
			}, []string{"query"}),
		},
		{
			Name: "find_notes_by_tag",
			Description: "Return all notes that carry every one of the specified tags (AND semantics). " +
				"Returns id, title, slug, tags, folder_id, updated_at — call get_note for full content. " +
				"Unlike search_notes, no text query is needed; this is a pure tag filter. " +
				"Use list_tags first to see what tags exist. " +
				"Example: find_notes_by_tag({tags: [\"recipe\", \"vegetarian\"]}) returns only notes tagged with both.",
			InputSchema: jsonSchema(map[string]any{
				"tags": strArrayProp("One or more tag strings. Only notes that have ALL of these tags are returned."),
			}, []string{"tags"}),
		},
		{
			Name: "list_tags",
			Description: "Return every unique tag in use across all of your notes, sorted alphabetically. " +
				"Use this to discover available tags before calling find_notes_by_tag or search_notes with a tag filter. " +
				"Returns a plain array of strings: [\"cooking\", \"ideas\", \"project\", ...].",
			InputSchema: jsonSchema(nil, nil),
		},
		// ── Note write tools ──────────────────────────────────────────────────────
		{
			Name: "create_note",
			Description: "Create a new markdown note and optionally set its content, folder, and tags in one call. " +
				"Returns the full note object including the newly assigned id. " +
				"Requires a readwrite API token. " +
				"The note file is written to disk immediately as a .md file in the notes directory. " +
				"If folder_id is omitted the note is created in the root (unfiled) area. " +
				"Titles must be unique within the same folder; duplicate titles return a 409 conflict error.",
			InputSchema: jsonSchema(map[string]any{
				"title":     prop("string", "Note title. Required. Must be unique within the target folder. 1–500 characters."),
				"content":   prop("string", "Initial markdown body. Optional — the note is created empty if omitted."),
				"folder_id": prop("integer", "ID of the folder to create the note in. Omit or pass null to create at root."),
				"tags":      strArrayProp("Tags to attach to the note. Optional."),
			}, []string{"title"}),
		},
		{
			Name: "update_note",
			Description: "Replace the full markdown content of an existing note. " +
				"Fetches the current content hash automatically to handle optimistic concurrency — no need to pass a hash. " +
				"Returns the updated note object with the new content_hash. " +
				"Requires a readwrite API token. " +
				"This replaces the entire content; it does not append or patch. To append, call get_note first, modify the text, then call update_note with the combined result. " +
				"Title and tags are not changed by this tool — use rename_note to update those.",
			InputSchema: jsonSchema(map[string]any{
				"id":      prop("integer", "ID of the note to update."),
				"content": prop("string", "New markdown content. This replaces the entire existing content."),
			}, []string{"id", "content"}),
		},
		{
			Name: "rename_note",
			Description: "Update the title and/or tags of a note without touching its content. " +
				"Returns the updated note metadata (does not include full content — call get_note if you need it). " +
				"Requires a readwrite API token. " +
				"Supply at least one of title or tags. Passing an empty tags array clears all tags.",
			InputSchema: jsonSchema(map[string]any{
				"id":    prop("integer", "ID of the note to update."),
				"title": prop("string", "New title. Must be unique within the note's current folder. Omit to leave unchanged."),
				"tags":  strArrayProp("New tag list. Replaces all existing tags. Pass [] to clear tags. Omit to leave unchanged."),
			}, []string{"id"}),
		},
		{
			Name: "move_note",
			Description: "Move a note to a different folder, or to the root (unfiled) area. " +
				"Renames the underlying .md file on disk to reflect the new location. " +
				"Returns a confirmation message with the note id and new folder id. " +
				"Requires a readwrite API token. " +
				"Use list_folders or find_folders to find the target folder id.",
			InputSchema: jsonSchema(map[string]any{
				"id":        prop("integer", "ID of the note to move."),
				"folder_id": prop("integer", "ID of the destination folder. Pass null or omit to move the note to root."),
			}, []string{"id"}),
		},
		{
			Name: "delete_note",
			Description: "Permanently delete a note and remove its .md file from disk. This cannot be undone. " +
				"Returns a confirmation message. " +
				"Requires a readwrite API token. " +
				"If git history is enabled on this server, the deletion is recorded as a git commit.",
			InputSchema: jsonSchema(map[string]any{
				"id": prop("integer", "ID of the note to delete."),
			}, []string{"id"}),
		},
		// ── Folder write tools ────────────────────────────────────────────────────
		{
			Name: "create_folder",
			Description: "Create a new folder, optionally nested inside an existing folder. " +
				"Returns the created folder object with id, parent_id, name, and disk_path. " +
				"Requires a readwrite API token. " +
				"Folder names must be unique within the same parent. " +
				"To build a nested path like 'Work/Projects/Q3', create each level in order: " +
				"first create 'Work', then create 'Projects' with parent_id set to Work's id, and so on. " +
				"Or use find_folders to check whether the intermediate folders already exist.",
			InputSchema: jsonSchema(map[string]any{
				"name":      prop("string", "Folder name. Must be unique within the parent (or root). Cannot contain / or .."),
				"parent_id": prop("integer", "ID of the parent folder. Omit or pass null to create a top-level folder."),
			}, []string{"name"}),
		},
		{
			Name: "rename_folder",
			Description: "Rename an existing folder. " +
				"Renames the directory on disk and updates all affected note disk paths in the database atomically. " +
				"Returns a confirmation message. " +
				"Requires a readwrite API token. " +
				"The new name must be unique among siblings (folders with the same parent).",
			InputSchema: jsonSchema(map[string]any{
				"id":   prop("integer", "ID of the folder to rename."),
				"name": prop("string", "New name for the folder. Cannot contain / or .."),
			}, []string{"id", "name"}),
		},
		{
			Name: "move_folder",
			Description: "Move a folder to a different parent folder, or to the top level. " +
				"All descendant folders and notes move with it; disk paths are updated atomically. " +
				"Returns a confirmation message. " +
				"Requires a readwrite API token. " +
				"Circular moves are rejected (e.g. moving a folder into one of its own descendants). " +
				"Use list_folders to inspect the current hierarchy before moving.",
			InputSchema: jsonSchema(map[string]any{
				"id":        prop("integer", "ID of the folder to move."),
				"parent_id": prop("integer", "ID of the new parent folder. Pass null or omit to move the folder to the top level."),
			}, []string{"id"}),
		},
		{
			Name: "delete_folder",
			Description: "Delete a folder and everything inside it — all notes and subfolders are permanently removed. This cannot be undone. " +
				"Returns a confirmation message. " +
				"Requires a readwrite API token. " +
				"The corresponding directory and all .md files inside it are deleted from disk.",
			InputSchema: jsonSchema(map[string]any{
				"id": prop("integer", "ID of the folder to delete."),
			}, []string{"id"}),
		},
	}
	return rpcOK(req.ID, map[string]any{"tools": tools})
}

// writeTools is the set of tool names that require write permission.
var writeTools = map[string]bool{
	"create_note":   true,
	"update_note":   true,
	"rename_note":   true,
	"move_note":     true,
	"delete_note":   true,
	"create_folder": true,
	"rename_folder": true,
	"move_folder":   true,
	"delete_folder": true,
}

const rpcErrForbidden = -32001

func (h *MCPHandler) handleToolsCall(r *http.Request, req rpcRequest, user *model.User) rpcResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcErr(req.ID, rpcInvalidParams, "invalid params")
	}

	authz := auth.AuthzFromContext(r.Context())
	// Enforce global scope: read-only tokens can never call write tools,
	// regardless of folder-level permissions. A token's Scope is the coarse
	// gate; folder permissions narrow it further.
	if writeTools[params.Name] && user.TokenScope == "read" {
		return rpcErr(req.ID, rpcErrForbidden, "this API token is read-only — create a readwrite token to use "+params.Name)
	}

	userID := user.ID
	userUUID := user.UUID
	ctx := r.Context()
	folders := h.notes.Folders()

	// checkRead and checkWrite resolve the token's permission for a given
	// folder id (nil = root). They return a rpcResponse error on deny, or
	// an empty rpcResponse (zero value) on allow.
	checkRead := func(folderID *int64) *rpcResponse {
		if authz == nil {
			return nil
		}
		ok, err := authz.CanRead(ctx, folders, userID, folderID)
		if err != nil {
			resp := rpcErr(req.ID, rpcInternalError, "authz check failed")
			return &resp
		}
		if !ok {
			resp := rpcErr(req.ID, rpcErrForbidden, "this API token does not have read access to the target folder")
			return &resp
		}
		return nil
	}
	checkWrite := func(folderID *int64) *rpcResponse {
		if authz == nil {
			return nil
		}
		ok, err := authz.CanWrite(ctx, folders, userID, folderID)
		if err != nil {
			resp := rpcErr(req.ID, rpcInternalError, "authz check failed")
			return &resp
		}
		if !ok {
			resp := rpcErr(req.ID, rpcErrForbidden, "this API token does not have write access to the target folder")
			return &resp
		}
		return nil
	}
	_ = checkRead
	_ = checkWrite

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
		results, err = filterSearchResults(ctx, authz, folders, userID, h.notes, results)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "authz filter failed")
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
		if resp := checkRead(note.FolderID); resp != nil {
			return *resp
		}
		text, _ := json.Marshal(note)
		return rpcOK(req.ID, toolResult(string(text)))

	case "list_notes":
		var args struct {
			FolderID *int64 `json:"folder_id"`
		}
		_ = json.Unmarshal(params.Arguments, &args)
		if args.FolderID != nil {
			if resp := checkRead(args.FolderID); resp != nil {
				return *resp
			}
			items, err := h.notes.ListNotes(ctx, userID, args.FolderID)
			if err != nil {
				return rpcErr(req.ID, rpcInternalError, "list failed")
			}
			text, _ := json.Marshal(items)
			return rpcOK(req.ID, toolResult(string(text)))
		}
		items, err := h.notes.ListAllNotes(ctx, userID)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "list failed")
		}
		items, err = filterNoteListItems(ctx, authz, folders, userID, items)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "authz filter failed")
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
		if resp := checkWrite(args.FolderID); resp != nil {
			return *resp
		}
		note, err := h.notes.CreateNote(ctx, userID, userUUID, args.FolderID, args.Title, args.Tags)
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
		if resp := checkWrite(note.FolderID); resp != nil {
			return *resp
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
		tree, err := h.notes.FolderTree(ctx, userID)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "list failed")
		}
		tree = filterFolderTree(authz, tree)
		text, _ := json.Marshal(tree)
		return rpcOK(req.ID, toolResult(string(text)))

	case "find_folders":
		var args struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.Query == "" {
			return rpcErr(req.ID, rpcInvalidParams, "query is required")
		}
		found, err := h.notes.FindFoldersByName(ctx, userID, args.Query)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "search failed")
		}
		found = filterFolderTree(authz, found)
		text, _ := json.Marshal(found)
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
		items, err = filterNoteListItems(ctx, authz, folders, userID, items)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "authz filter failed")
		}
		text, _ := json.Marshal(items)
		return rpcOK(req.ID, toolResult(string(text)))

	case "list_tags":
		tags, err := h.notes.ListAllTags(ctx, userID)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "list failed")
		}
		// Only expose tags that appear on at least one readable note.
		if authz != nil && authz.Scoped {
			all, err := h.notes.ListAllNotes(ctx, userID)
			if err != nil {
				return rpcErr(req.ID, rpcInternalError, "list failed")
			}
			readable, err := filterNoteListItems(ctx, authz, folders, userID, all)
			if err != nil {
				return rpcErr(req.ID, rpcInternalError, "authz filter failed")
			}
			seen := make(map[string]struct{}, len(tags))
			for _, n := range readable {
				for _, t := range n.Tags {
					seen[t] = struct{}{}
				}
			}
			kept := make([]string, 0, len(tags))
			for _, t := range tags {
				if _, ok := seen[t]; ok {
					kept = append(kept, t)
				}
			}
			tags = kept
		}
		text, _ := json.Marshal(tags)
		return rpcOK(req.ID, toolResult(string(text)))

	case "rename_note":
		var args struct {
			ID    int64    `json:"id"`
			Title *string  `json:"title"`
			Tags  []string `json:"tags"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.ID == 0 {
			return rpcErr(req.ID, rpcInvalidParams, "id is required")
		}
		if args.Title == nil && args.Tags == nil {
			return rpcErr(req.ID, rpcInvalidParams, "at least one of title or tags must be provided")
		}
		note, err := h.notes.GetNote(ctx, userID, args.ID)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, errorString(err))
		}
		if resp := checkWrite(note.FolderID); resp != nil {
			return *resp
		}
		newTitle := note.Title
		if args.Title != nil {
			newTitle = *args.Title
		}
		newTags := note.Tags
		if args.Tags != nil {
			newTags = args.Tags
		}
		if err := h.notes.UpdateNoteMetadata(ctx, userID, userUUID, args.ID, newTitle, newTags); err != nil {
			return rpcErr(req.ID, rpcInternalError, errorString(err))
		}
		return rpcOK(req.ID, toolResult(fmt.Sprintf(`{"id":%d,"title":%q,"tags":%s}`, args.ID, newTitle, mustJSON(newTags))))

	case "move_note":
		var args struct {
			ID       int64  `json:"id"`
			FolderID *int64 `json:"folder_id"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.ID == 0 {
			return rpcErr(req.ID, rpcInvalidParams, "id is required")
		}
		note, err := h.notes.GetNote(ctx, userID, args.ID)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, errorString(err))
		}
		if resp := checkWrite(note.FolderID); resp != nil {
			return *resp
		}
		if resp := checkWrite(args.FolderID); resp != nil {
			return *resp
		}
		if err := h.notes.MoveNote(ctx, userID, userUUID, args.ID, args.FolderID); err != nil {
			return rpcErr(req.ID, rpcInternalError, errorString(err))
		}
		folderMsg := "root"
		if args.FolderID != nil {
			folderMsg = fmt.Sprintf("folder %d", *args.FolderID)
		}
		return rpcOK(req.ID, toolResult(fmt.Sprintf("note %d moved to %s", args.ID, folderMsg)))

	case "delete_note":
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
		if resp := checkWrite(note.FolderID); resp != nil {
			return *resp
		}
		if err := h.notes.DeleteNote(ctx, userID, args.ID); err != nil {
			return rpcErr(req.ID, rpcInternalError, errorString(err))
		}
		return rpcOK(req.ID, toolResult(fmt.Sprintf("note %d deleted", args.ID)))

	case "create_folder":
		var args struct {
			Name     string `json:"name"`
			ParentID *int64 `json:"parent_id"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.Name == "" {
			return rpcErr(req.ID, rpcInvalidParams, "name is required")
		}
		if resp := checkWrite(args.ParentID); resp != nil {
			return *resp
		}
		folder, err := h.notes.CreateFolder(ctx, userID, userUUID, args.ParentID, args.Name)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, errorString(err))
		}
		text, _ := json.Marshal(folder)
		return rpcOK(req.ID, toolResult(string(text)))

	case "rename_folder":
		var args struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.ID == 0 || args.Name == "" {
			return rpcErr(req.ID, rpcInvalidParams, "id and name are required")
		}
		if resp := checkWrite(&args.ID); resp != nil {
			return *resp
		}
		if err := h.notes.RenameFolder(ctx, userID, userUUID, args.ID, args.Name); err != nil {
			return rpcErr(req.ID, rpcInternalError, errorString(err))
		}
		return rpcOK(req.ID, toolResult(fmt.Sprintf("folder %d renamed to %q", args.ID, args.Name)))

	case "move_folder":
		var args struct {
			ID       int64  `json:"id"`
			ParentID *int64 `json:"parent_id"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.ID == 0 {
			return rpcErr(req.ID, rpcInvalidParams, "id is required")
		}
		if resp := checkWrite(&args.ID); resp != nil {
			return *resp
		}
		if resp := checkWrite(args.ParentID); resp != nil {
			return *resp
		}
		if err := h.notes.MoveFolder(ctx, userID, userUUID, args.ID, args.ParentID); err != nil {
			return rpcErr(req.ID, rpcInternalError, errorString(err))
		}
		parentMsg := "top level"
		if args.ParentID != nil {
			parentMsg = fmt.Sprintf("folder %d", *args.ParentID)
		}
		return rpcOK(req.ID, toolResult(fmt.Sprintf("folder %d moved to %s", args.ID, parentMsg)))

	case "delete_folder":
		var args struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil || args.ID == 0 {
			return rpcErr(req.ID, rpcInvalidParams, "id is required")
		}
		if resp := checkWrite(&args.ID); resp != nil {
			return *resp
		}
		if err := h.notes.DeleteFolder(ctx, userID, args.ID); err != nil {
			return rpcErr(req.ID, rpcInternalError, errorString(err))
		}
		return rpcOK(req.ID, toolResult(fmt.Sprintf("folder %d and all its contents deleted", args.ID)))

	default:
		return rpcErr(req.ID, rpcMethodNotFound, "unknown tool: "+params.Name)
	}
}

func (h *MCPHandler) handleResourcesList(r *http.Request, req rpcRequest, userID int64) rpcResponse {
	ctx := r.Context()
	items, err := h.notes.ListAllNotes(ctx, userID)
	if err != nil {
		return rpcErr(req.ID, rpcInternalError, "list notes failed")
	}
	authz := auth.AuthzFromContext(ctx)
	items, err = filterNoteListItems(ctx, authz, h.notes.Folders(), userID, items)
	if err != nil {
		return rpcErr(req.ID, rpcInternalError, "authz filter failed")
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

	ctx := r.Context()
	note, err := h.notes.GetNote(ctx, userID, id)
	if err != nil {
		return rpcErr(req.ID, rpcInternalError, errorString(err))
	}
	authz := auth.AuthzFromContext(ctx)
	if authz != nil {
		ok, err := authz.CanRead(ctx, h.notes.Folders(), userID, note.FolderID)
		if err != nil {
			return rpcErr(req.ID, rpcInternalError, "authz check failed")
		}
		if !ok {
			return rpcErr(req.ID, rpcErrForbidden, "this API token does not have read access to the target folder")
		}
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

func strArrayProp(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"description": description,
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
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

// ── Permission filtering helpers ──────────────────────────────────────────────

// filterNoteListItems drops notes the token has no read access to. It caches
// per-folder-id decisions so each folder is resolved at most once per call.
func filterNoteListItems(
	ctx context.Context,
	authz *auth.TokenAuthz,
	folders repository.FolderRepository,
	userID int64,
	items []*model.NoteListItem,
) ([]*model.NoteListItem, error) {
	if authz == nil || !authz.Scoped {
		// Global scope already gated by token.Scope at the call site.
		return items, nil
	}
	cache := map[int64]bool{} // folder_id → canRead
	rootDecided, rootAllow := false, false

	out := make([]*model.NoteListItem, 0, len(items))
	for _, n := range items {
		if n.FolderID == nil {
			if !rootDecided {
				ok, err := authz.CanRead(ctx, folders, userID, nil)
				if err != nil {
					return nil, err
				}
				rootDecided, rootAllow = true, ok
			}
			if rootAllow {
				out = append(out, n)
			}
			continue
		}
		if allow, ok := cache[*n.FolderID]; ok {
			if allow {
				out = append(out, n)
			}
			continue
		}
		ok, err := authz.CanRead(ctx, folders, userID, n.FolderID)
		if err != nil {
			return nil, err
		}
		cache[*n.FolderID] = ok
		if ok {
			out = append(out, n)
		}
	}
	return out, nil
}

// filterSearchResults drops search hits the token cannot read. It must load
// each hit's folder_id (the SearchResult doesn't carry it directly), but
// caches the per-folder decision.
func filterSearchResults(
	ctx context.Context,
	authz *auth.TokenAuthz,
	folders repository.FolderRepository,
	userID int64,
	notesSvc *notes.Service,
	hits []*model.SearchResult,
) ([]*model.SearchResult, error) {
	if authz == nil || !authz.Scoped {
		return hits, nil
	}
	cache := map[int64]bool{}
	rootDecided, rootAllow := false, false
	out := make([]*model.SearchResult, 0, len(hits))
	for _, h := range hits {
		note, err := notesSvc.GetNote(ctx, userID, h.NoteID)
		if err != nil {
			continue
		}
		if note.FolderID == nil {
			if !rootDecided {
				ok, err := authz.CanRead(ctx, folders, userID, nil)
				if err != nil {
					return nil, err
				}
				rootDecided, rootAllow = true, ok
			}
			if rootAllow {
				out = append(out, h)
			}
			continue
		}
		if allow, ok := cache[*note.FolderID]; ok {
			if allow {
				out = append(out, h)
			}
			continue
		}
		ok, err := authz.CanRead(ctx, folders, userID, note.FolderID)
		if err != nil {
			return nil, err
		}
		cache[*note.FolderID] = ok
		if ok {
			out = append(out, h)
		}
	}
	return out, nil
}

// filterFolderTree drops folders the token cannot read. Child folders whose
// ancestor has a grant remain visible (FilterReadableFolderIDs resolves the
// chain via the passed tree itself, not the repository).
func filterFolderTree(authz *auth.TokenAuthz, tree []*model.FolderTreeItem) []*model.FolderTreeItem {
	if authz == nil || !authz.Scoped {
		return tree
	}
	readable, _ := authz.FilterReadableFolderIDs(tree)
	out := make([]*model.FolderTreeItem, 0, len(tree))
	for _, f := range tree {
		if readable[f.ID] {
			out = append(out, f)
		}
	}
	return out
}
