package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/notes"
)

type NotesHandler struct {
	svc *notes.Service
}

func NewNotesHandler(svc *notes.Service) *NotesHandler {
	return &NotesHandler{svc: svc}
}

type createNoteRequest struct {
	FolderID *int64   `json:"folder_id"`
	Title    string   `json:"title"`
	Tags     []string `json:"tags"`
}

func (h *NotesHandler) Create(c *gin.Context) {
	user := ginUser(c)
	var req createNoteRequest
	if err := readJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	note, err := h.svc.CreateNote(c.Request.Context(), user.ID, user.UUID, req.FolderID, req.Title, req.Tags)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusCreated, note)
}

func (h *NotesHandler) Get(c *gin.Context) {
	user := ginUser(c)
	noteID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "note id must be a positive integer"})
		return
	}

	note, err := h.svc.GetNote(c.Request.Context(), user.ID, noteID)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, note)
}

type patchNoteRequest struct {
	Content     *string  `json:"content"`
	ContentHash *string  `json:"content_hash"` // required when patching content
	Title       *string  `json:"title"`
	Tags        []string `json:"tags"`
}

func (h *NotesHandler) Patch(c *gin.Context) {
	user := ginUser(c)
	noteID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "note id must be a positive integer"})
		return
	}

	var req patchNoteRequest
	if err := readJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Update content if provided.
	if req.Content != nil {
		if req.ContentHash == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "content_hash required when patching content"})
			return
		}
		newHash, err := h.svc.UpdateNoteContent(c.Request.Context(), user.ID, noteID, *req.Content, *req.ContentHash)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"content_hash": newHash})
		return
	}

	// Update metadata if provided.
	title := ""
	if req.Title != nil {
		title = *req.Title
	}
	if err := h.svc.UpdateNoteMetadata(c.Request.Context(), user.ID, noteID, title, req.Tags); err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "updated"})
}

type moveNoteRequest struct {
	FolderID *int64 `json:"folder_id"` // null = move to root
}

func (h *NotesHandler) Move(c *gin.Context) {
	user := ginUser(c)
	noteID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "note id must be a positive integer"})
		return
	}

	var req moveNoteRequest
	if err := readJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.MoveNote(c.Request.Context(), user.ID, user.UUID, noteID, req.FolderID); err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "moved"})
}

func (h *NotesHandler) Delete(c *gin.Context) {
	user := ginUser(c)
	noteID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "note id must be a positive integer"})
		return
	}

	if err := h.svc.DeleteNote(c.Request.Context(), user.ID, noteID); err != nil {
		writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

type shareRequest struct {
	Clear bool `json:"clear"`
}

func (h *NotesHandler) Share(c *gin.Context) {
	user := ginUser(c)
	noteID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "note id must be a positive integer"})
		return
	}

	var req shareRequest
	_ = readJSON(c, &req) // optional body

	token, err := h.svc.SetShareToken(c.Request.Context(), user.ID, noteID, req.Clear)
	if err != nil {
		writeError(c, err)
		return
	}

	if token == nil {
		c.JSON(http.StatusOK, gin.H{"share_token": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"share_token": *token})
}

func (h *NotesHandler) ListRoot(c *gin.Context) {
	user := ginUser(c)
	items, err := h.svc.ListNotes(c.Request.Context(), user.ID, nil)
	if err != nil {
		writeError(c, err)
		return
	}
	if items == nil {
		items = []*model.NoteListItem{}
	}
	c.JSON(http.StatusOK, items)
}

func (h *NotesHandler) ListAll(c *gin.Context) {
	user := ginUser(c)
	items, err := h.svc.ListAllNotes(c.Request.Context(), user.ID)
	if err != nil {
		writeError(c, err)
		return
	}
	if items == nil {
		items = []*model.NoteListItem{}
	}
	c.JSON(http.StatusOK, items)
}

// Context assembles a concatenated markdown string from the user's notes for
// use as LLM prompt context. Optional query param: folder_id (integer).
func (h *NotesHandler) Context(c *gin.Context) {
	user := ginUser(c)

	var folderID *int64
	if raw := c.Query("folder_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "folder_id must be an integer"})
			return
		}
		folderID = &id
	}

	result, err := h.svc.NoteContext(c.Request.Context(), user.ID, folderID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *NotesHandler) Search(c *gin.Context) {
	user := ginUser(c)
	q := c.Query("q")
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "q parameter required"})
		return
	}

	tags := c.QueryArray("tag")

	results, err := h.svc.Search(c.Request.Context(), user.ID, q, tags)
	if err != nil {
		writeError(c, err)
		return
	}
	if results == nil {
		results = []*model.SearchResult{}
	}
	c.JSON(http.StatusOK, results)
}
