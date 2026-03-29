package handler

import (
	"net/http"

	"github.com/th0rn0/thornotes/internal/auth"
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

func (h *NotesHandler) Create(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	var req createNoteRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	note, err := h.svc.CreateNote(r.Context(), user.ID, req.FolderID, req.Title, req.Tags)
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, note)
}

func (h *NotesHandler) Get(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	noteID, err := pathParamInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid note id"})
		return
	}

	note, err := h.svc.GetNote(r.Context(), user.ID, noteID)
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, note)
}

type patchNoteRequest struct {
	Content      *string  `json:"content"`
	ContentHash  *string  `json:"content_hash"` // required when patching content
	Title        *string  `json:"title"`
	Tags         []string `json:"tags"`
}

func (h *NotesHandler) Patch(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	noteID, err := pathParamInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid note id"})
		return
	}

	var req patchNoteRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Update content if provided.
	if req.Content != nil {
		if req.ContentHash == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content_hash required when patching content"})
			return
		}
		newHash, err := h.svc.UpdateNoteContent(r.Context(), user.ID, noteID, *req.Content, *req.ContentHash)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"content_hash": newHash})
		return
	}

	// Update metadata if provided.
	title := ""
	if req.Title != nil {
		title = *req.Title
	}
	if err := h.svc.UpdateNoteMetadata(r.Context(), user.ID, noteID, title, req.Tags); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
}

func (h *NotesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	noteID, err := pathParamInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid note id"})
		return
	}

	if err := h.svc.DeleteNote(r.Context(), user.ID, noteID); err != nil {
		writeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type shareRequest struct {
	Clear bool `json:"clear"`
}

func (h *NotesHandler) Share(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	noteID, err := pathParamInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid note id"})
		return
	}

	var req shareRequest
	_ = readJSON(r, &req) // optional body

	token, err := h.svc.SetShareToken(r.Context(), user.ID, noteID, req.Clear)
	if err != nil {
		writeError(w, err)
		return
	}

	if token == nil {
		writeJSON(w, http.StatusOK, map[string]any{"share_token": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"share_token": *token})
}

func (h *NotesHandler) ListRoot(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	items, err := h.svc.ListNotes(r.Context(), user.ID, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	if items == nil {
		items = []*model.NoteListItem{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *NotesHandler) Search(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q parameter required"})
		return
	}

	var tags []string
	if tagParam := r.URL.Query()["tag"]; tagParam != nil {
		tags = tagParam
	}

	results, err := h.svc.Search(r.Context(), user.ID, q, tags)
	if err != nil {
		writeError(w, err)
		return
	}
	if results == nil {
		results = []*model.SearchResult{}
	}
	writeJSON(w, http.StatusOK, results)
}
