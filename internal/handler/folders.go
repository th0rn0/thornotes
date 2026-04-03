package handler

import (
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

	items, err := h.svc.ListNotes(c.Request.Context(), user.ID, &folderID)
	if err != nil {
		writeError(c, err)
		return
	}
	if items == nil {
		items = []*model.NoteListItem{}
	}
	c.JSON(http.StatusOK, items)
}
