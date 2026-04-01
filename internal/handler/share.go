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
