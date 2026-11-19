package handler

import (
	"errors"
	"io"
	"net/http"
	"time"

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
// It mirrors the note-share request exactly: `clear` revokes, and `expires_at`
// is the opt-in deadline, so the two share routes cannot be told apart by their
// payload. Omitted or null expires_at means no expiry — the caller has to ask for
// a deadline, and nothing here invents one.
type folderShareRequest struct {
	// Clear revokes the link (the row is deleted). It is the same word, with the
	// same meaning, as on POST /api/v1/notes/:id/share — two share routes ten
	// lines apart that disagreed about what `clear` does is the trap ticket 601
	// wrote itself a note about.
	Clear bool `json:"clear"`
	// ExpiresAt is the deadline for the link the request creates, as RFC3339. It
	// applies to the NEW link: a rotation clears the previous deadline, so a
	// caller who wants the old link's date kept has to send it again (that is
	// what POST has always meant on both surfaces).
	ExpiresAt *time.Time `json:"expires_at"`
}

// folderShareExpiryRequest is the body of PATCH /api/v1/folders/:id/share: the
// same two shapes the note-share route accepts, and the same reasoning for the
// explicit `clear_expiry`.
type folderShareExpiryRequest struct {
	ExpiresAt *time.Time `json:"expires_at"`
	// ClearExpiry removes the deadline instead of setting one. The field is
	// deliberately NOT called "clear", which the sibling POST route uses to
	// REVOKE the token: two routes ten lines apart where the same word means
	// opposite things on a security-relevant field is how an operator trying to
	// kill a leaked link makes it permanent instead.
	ClearExpiry bool `json:"clear_expiry"`
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

	// A deadline in the past would publish a link that is already dead: the
	// caller would get a token that answers 404 everywhere, with nothing in the
	// response saying why. The note route refuses it for the same reason, and
	// the two must agree or one of them hands out a stillborn URL.
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at must be in the future"})
		return
	}

	token, err := h.svc.SetFolderShareToken(c.Request.Context(), user.ID, folderID, req.Clear, req.ExpiresAt)
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

// PatchShareExpiry changes the deadline of a folder's existing link without
// rotating its token, so URLs already handed out keep working.
//
// Re-sharing (POST /api/v1/folders/:id/share) generates a new token, which is
// what a user wants when a link leaked and the opposite of what they want when
// they merely need to extend or drop a deadline. Without this route, the only way
// to change a folder link's expiry would revoke every copy of it in circulation.
//
// An expired link is refused with 409 by the service (see
// notes.SetFolderShareExpiry) — and so is clearing its deadline, because nulling
// it would make a dead link immortal.
func (h *FoldersHandler) PatchShareExpiry(c *gin.Context) {
	user := ginUser(c)
	folderID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder id must be a positive integer"})
		return
	}

	var req folderShareExpiryRequest
	if err := readJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "request body must be a JSON object: " + err.Error()})
		return
	}

	var expiresAt *time.Time
	switch {
	case req.ClearExpiry:
		expiresAt = nil
	case req.ExpiresAt != nil:
		if !req.ExpiresAt.After(time.Now()) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at must be in the future"})
			return
		}
		expiresAt = req.ExpiresAt
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at or clear_expiry is required"})
		return
	}

	if err := h.svc.SetFolderShareExpiry(c.Request.Context(), user.ID, folderID, expiresAt); err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"expires_at": expiresAt})
}
