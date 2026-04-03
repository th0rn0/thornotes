package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/th0rn0/thornotes/internal/notes"
)

const importMaxBytes = 10 << 20 // 10 MB

type ImportHandler struct {
	svc *notes.Service
}

func NewImportHandler(svc *notes.Service) *ImportHandler {
	return &ImportHandler{svc: svc}
}

// Import accepts a multipart/form-data upload with a single "file" field.
// Accepts .md files or .zip archives containing .md files.
func (h *ImportHandler) Import(c *gin.Context) {
	user := ginUser(c)

	if err := c.Request.ParseMultipartForm(importMaxBytes); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "request too large or not multipart (max 10 MB)"})
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing \"file\" field in multipart form"})
		return
	}
	defer file.Close()

	if header.Size > importMaxBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file exceeds 10 MB limit"})
		return
	}

	name := header.Filename
	lower := strings.ToLower(name)

	ctx := c.Request.Context()

	switch {
	case strings.HasSuffix(lower, ".md"):
		buf := make([]byte, header.Size)
		if _, err := file.Read(buf); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "could not read file"})
			return
		}
		result, err := h.svc.ImportMarkdown(ctx, user.ID, user.UUID, name, string(buf))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)

	case strings.HasSuffix(lower, ".zip"):
		buf := make([]byte, header.Size)
		if _, err := file.Read(buf); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "could not read file"})
			return
		}
		result, err := h.svc.ImportZip(ctx, user.ID, user.UUID, buf)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported file type — only .md and .zip are accepted"})
	}
}
