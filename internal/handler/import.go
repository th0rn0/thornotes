package handler

import (
	"errors"
	"io"
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

	// Cap the total request body before parsing. ParseMultipartForm's argument
	// only bounds the in-memory portion; without MaxBytesReader the parser
	// will happily spill the rest to disk in temp files, which is a disk-fill
	// vector. The +1KiB slack lets a fully-utilised file payload pass while
	// still rejecting clearly oversized requests.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, importMaxBytes+(1<<10))

	if err := c.Request.ParseMultipartForm(importMaxBytes); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request exceeds 10 MB limit"})
			return
		}
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

	// Read the full file body. io.ReadAll combined with the MaxBytesReader
	// above guarantees we either get the full payload or a clear EOF/limit
	// error — never a silently truncated buffer.
	buf, err := io.ReadAll(io.LimitReader(file, importMaxBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not read file"})
		return
	}
	if len(buf) > importMaxBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file exceeds 10 MB limit"})
		return
	}

	switch {
	case strings.HasSuffix(lower, ".md"):
		result, err := h.svc.ImportMarkdown(ctx, user.ID, user.UUID, name, string(buf))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)

	case strings.HasSuffix(lower, ".zip"):
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
