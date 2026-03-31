package handler

import (
	"html/template"
	"net/http"

	"github.com/th0rn0/thornotes/internal/notes"
)

// ShareHandler serves the public shared note view.
type ShareHandler struct {
	svc  *notes.Service
	tmpl *template.Template
}

func NewShareHandler(svc *notes.Service, tmpl *template.Template) *ShareHandler {
	return &ShareHandler{svc: svc, tmpl: tmpl}
}

func (h *ShareHandler) View(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.NotFound(w, r)
		return
	}

	note, err := h.svc.GetNoteByShareToken(r.Context(), token)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "share.html", note); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}
