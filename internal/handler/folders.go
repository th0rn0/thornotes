package handler

import (
	"net/http"
	"strconv"

	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/notes"
)

type FoldersHandler struct {
	svc *notes.Service
}

func NewFoldersHandler(svc *notes.Service) *FoldersHandler {
	return &FoldersHandler{svc: svc}
}

func (h *FoldersHandler) List(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	tree, err := h.svc.FolderTree(r.Context(), user.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if tree == nil {
		tree = []*model.FolderTreeItem{}
	}
	writeJSON(w, http.StatusOK, tree)
}

type createFolderRequest struct {
	ParentID *int64 `json:"parent_id"`
	Name     string `json:"name"`
}

func (h *FoldersHandler) Create(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	var req createFolderRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	folder, err := h.svc.CreateFolder(r.Context(), user.ID, req.ParentID, req.Name)
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, folder)
}

type renameFolderRequest struct {
	Name string `json:"name"`
}

func (h *FoldersHandler) Rename(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	folderID, err := pathParamInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid folder id"})
		return
	}

	var req renameFolderRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if err := h.svc.RenameFolder(r.Context(), user.ID, folderID, req.Name); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "renamed"})
}

func (h *FoldersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	folderID, err := pathParamInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid folder id"})
		return
	}

	if err := h.svc.DeleteFolder(r.Context(), user.ID, folderID); err != nil {
		writeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *FoldersHandler) ListNotes(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	folderID, err := pathParamInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid folder id"})
		return
	}

	items, err := h.svc.ListNotes(r.Context(), user.ID, &folderID)
	if err != nil {
		writeError(w, err)
		return
	}
	if items == nil {
		items = []*model.NoteListItem{}
	}
	writeJSON(w, http.StatusOK, items)
}

// pathParamInt64 extracts a named path parameter from Go 1.22 ServeMux.
func pathParamInt64(r *http.Request, name string) (int64, error) {
	s := r.PathValue(name)
	return strconv.ParseInt(s, 10, 64)
}
