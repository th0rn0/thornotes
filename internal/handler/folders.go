package handler

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/notes"
)

type FoldersHandler struct {
	svc *notes.Service
}

func NewFoldersHandler(svc *notes.Service) *FoldersHandler {
	return &FoldersHandler{svc: svc}
}

func (h *FoldersHandler) List(c *gin.Context) {
	user := ginUser(c)
	tree, err := h.svc.FolderTree(c.Request.Context(), user.ID)
	if err != nil {
		writeError(c, err)
		return
	}
	if tree == nil {
		tree = []*model.FolderTreeItem{}
	}
	c.JSON(http.StatusOK, tree)
}

type createFolderRequest struct {
	ParentID *int64 `json:"parent_id"`
	Name     string `json:"name"`
}

func (h *FoldersHandler) Create(c *gin.Context) {
	user := ginUser(c)
	var req createFolderRequest
	if err := readJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	folder, err := h.svc.CreateFolder(c.Request.Context(), user.ID, user.UUID, req.ParentID, req.Name)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusCreated, folder)
}

type renameFolderRequest struct {
	Name string `json:"name"`
}

func (h *FoldersHandler) Rename(c *gin.Context) {
	user := ginUser(c)
	folderID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder id must be a positive integer"})
		return
	}

	var req renameFolderRequest
	if err := readJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.RenameFolder(c.Request.Context(), user.ID, user.UUID, folderID, req.Name); err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "renamed"})
}

func (h *FoldersHandler) Delete(c *gin.Context) {
	user := ginUser(c)
	folderID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder id must be a positive integer"})
		return
	}

	if err := h.svc.DeleteFolder(c.Request.Context(), user.ID, folderID); err != nil {
		writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

type moveFolderRequest struct {
	ParentID *int64 `json:"parent_id"` // null = move to root
}

func (h *FoldersHandler) Move(c *gin.Context) {
	user := ginUser(c)
	folderID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder id must be a positive integer"})
		return
	}

	var req moveFolderRequest
	if err := readJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.MoveFolder(c.Request.Context(), user.ID, user.UUID, folderID, req.ParentID); err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "moved"})
}

func (h *FoldersHandler) ListNotes(c *gin.Context) {
	user := ginUser(c)
	folderID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder id must be a positive integer"})
		return
	}

	items, err := h.svc.FolderNotes(c.Request.Context(), user.ID, folderID)
	if err != nil {
		writeError(c, err)
		return
	}
	if items == nil {
		items = []*model.NoteListItem{}
	}
	c.JSON(http.StatusOK, items)
}

// folderShareRequest is the body of POST /api/v1/folders/:id/share.
//
// It mirrors the note-share request exactly, including what is NOT here: an
// expiry deadline is slice 2, so a payload carrying `expires_at` is rejected as
// an unknown field rather than silently ignored. A caller who thinks they set a
// deadline and did not would be told the link is immortal by accident.
type folderShareRequest struct {
	// Clear revokes the link (the row is deleted). It is the same word, with the
	// same meaning, as on POST /api/v1/notes/:id/share — two share routes ten
	// lines apart that disagreed about what `clear` does is the trap ticket 601
	// wrote itself a note about.
	Clear bool `json:"clear"`
}

// Share issues, rotates or revokes a folder's public link.
//
// Re-sharing rotates: POST without `clear` always mints a new token, even when
// the folder already had one, exactly as the note route does. The cost is real
// and deliberate — the second click kills the link that was just pasted into a
// chat — and it is the reason the confirmation in the UI has to say so. The
// alternative, idempotent semantics on one route and rotating on the other, is
// worse: a user cannot tell which link they have.
func (h *FoldersHandler) Share(c *gin.Context) {
	user := ginUser(c)
	folderID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder id must be a positive integer"})
		return
	}

	var req folderShareRequest
	// The body is optional — no body at all means "share" — but a body that is
	// present and does not decode must never be treated as one. The note route
	// shipped this bug once: `{"clear":"true"}` was unreadable, fell through to
	// the generate branch, and the caller was told the link was revoked while
	// being handed a fresh working one. Only io.EOF is tolerated.
	if err := readJSON(c, &req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "request body must be a JSON object: " + err.Error()})
		return
	}

	token, err := h.svc.SetFolderShareToken(c.Request.Context(), user.ID, folderID, req.Clear)
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
