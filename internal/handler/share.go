package handler

import (
	"html/template"
	"net/http"

	"github.com/gin-gonic/gin"
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

func (h *ShareHandler) View(c *gin.Context) {
	// A share URL is a bearer capability: whoever holds it reads the note, with no
	// session and no account. Two consequences for the response, and they apply to
	// every outcome of this handler — including the 404, which is the same
	// capability with a wrong token, so a cacheable 404 is the same disclosure in
	// the other direction:
	//
	//   no-store   nothing between here and the reader may keep a copy. A proxy that
	//              caches this serves the note to whoever asks for it next.
	//   noindex    if a share URL is ever pasted somewhere that gets crawled, the
	//              note must not enter a search index.
	c.Header("Cache-Control", "no-store")
	c.Header("X-Robots-Tag", "noindex")

	token := c.Param("token")
	if token == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	note, err := h.svc.GetNoteByShareToken(c.Request.Context(), token)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(c.Writer, "share.html", note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "render error"})
	}
}
