package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/th0rn0/thornotes/internal/notes"
)

// HistoryHandler serves note git-history endpoints.
type HistoryHandler struct {
	svc *notes.Service
}

// NewHistoryHandler creates a HistoryHandler.
func NewHistoryHandler(svc *notes.Service) *HistoryHandler {
	return &HistoryHandler{svc: svc}
}

// List returns the commit history for a note (newest first).
// Query param: limit (default 50, 0 = unlimited).
//
//	GET /api/v1/notes/:id/history
func (h *HistoryHandler) List(c *gin.Context) {
	user := ginUser(c)
	noteID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid note id"})
		return
	}

	limit := 50
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
			return
		}
		limit = n
	}

	entries, err := h.svc.NoteHistory(c.Request.Context(), user.ID, noteID, limit)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, entries)
}

// At returns a note's content at a specific git commit.
//
//	GET /api/v1/notes/:id/history/:sha
func (h *HistoryHandler) At(c *gin.Context) {
	user := ginUser(c)
	noteID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid note id"})
		return
	}

	sha := c.Param("sha")
	if len(sha) < 7 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sha"})
		return
	}

	entry, err := h.svc.NoteContentAt(c.Request.Context(), user.ID, noteID, sha)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, entry)
}

// Restore restores a note to the content it had at a specific git commit.
// The current content hash must be provided to guard against races.
//
//	POST /api/v1/notes/:id/history/:sha/restore
type restoreRequest struct {
	ContentHash string `json:"content_hash"`
}

func (h *HistoryHandler) Restore(c *gin.Context) {
	user := ginUser(c)
	noteID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid note id"})
		return
	}

	sha := c.Param("sha")
	if len(sha) < 7 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sha"})
		return
	}

	var req restoreRequest
	if err := readJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	newHash, err := h.svc.NoteRestoreAt(c.Request.Context(), user.ID, noteID, sha, req.ContentHash)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"content_hash": newHash})
}
