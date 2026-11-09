package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/notes"
)

// ExportHandler serves the two "download as zip" routes:
//
//	GET /api/v1/notes/:id/zip    one .md in a zip
//	GET /api/v1/folders/:id/zip  the folder and everything under it
//
// Both are reads, so neither takes a CSRF token: nothing is mutated.
type ExportHandler struct {
	svc *notes.Service
}

func NewExportHandler(svc *notes.Service) *ExportHandler {
	return &ExportHandler{svc: svc}
}

// Note streams a single-note archive.
func (h *ExportHandler) Note(c *gin.Context) {
	user := ginUser(c)
	noteID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "note id must be a positive integer"})
		return
	}

	plan, err := h.svc.PlanNoteExport(c.Request.Context(), user.ID, noteID)
	if err != nil {
		// A note that is not the caller's and one that does not exist are both
		// ErrNotFound, so this is the API's single not-found body either way.
		writeError(c, err)
		return
	}
	h.serve(c, plan)
}

// Folder streams an archive of the folder's subtree, rooted at a directory named
// after the folder so it re-imports as that folder.
func (h *ExportHandler) Folder(c *gin.Context) {
	user := ginUser(c)
	folderID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder id must be a positive integer"})
		return
	}

	plan, err := h.svc.PlanFolderExport(c.Request.Context(), user.ID, folderID)
	if err != nil {
		writeError(c, err)
		return
	}
	h.serve(c, plan)
}

// HeadNote answers the browser's preflight for the note archive.
//
// The UI checks with HEAD before navigating, because a navigation to a 404
// replaces the page with a JSON error body. That preflight has to be cheap:
// it resolves ownership and produces the same headers the GET would, and it
// never builds or streams an archive. Registering HEAD explicitly is required
// — Gin's GET does not answer HEAD, so without this the route falls through to
// NoRoute and every existing note would look missing.
func (h *ExportHandler) HeadNote(c *gin.Context) {
	user := ginUser(c)
	noteID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "note id must be a positive integer"})
		return
	}
	plan, err := h.svc.PlanNoteExport(c.Request.Context(), user.ID, noteID)
	if err != nil {
		writeError(c, err)
		return
	}
	h.headersOnly(c, plan)
}

// HeadFolder is the preflight twin of Folder.
func (h *ExportHandler) HeadFolder(c *gin.Context) {
	user := ginUser(c)
	folderID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder id must be a positive integer"})
		return
	}
	plan, err := h.svc.PlanFolderExport(c.Request.Context(), user.ID, folderID)
	if err != nil {
		writeError(c, err)
		return
	}
	h.headersOnly(c, plan)
}

// headersOnly emits exactly the headers serve() would, and no body.
func (h *ExportHandler) headersOnly(c *gin.Context, plan *notes.ExportPlan) {
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", `attachment; filename="`+plan.Filename+`"`)
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
}

// serve writes the archive straight to the response.
//
// Everything that can fail before the first byte — ownership, the subtree
// queries, the file-existence checks, the entry order — already happened in the
// plan, so the status line and headers here are final.
func (h *ExportHandler) serve(c *gin.Context, plan *notes.ExportPlan) {
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", `attachment; filename="`+plan.Filename+`"`)
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)

	if err := h.svc.StreamExport(c.Request.Context(), plan, c.Writer); err != nil {
		// Headers and possibly several entries are already on the wire, so the
		// status cannot be changed and no report can be inserted. Returning
		// without the central directory makes the archive invalid, which is the
		// honest outcome: the client's unzip fails loudly instead of opening a
		// short but valid archive that quietly lost notes.
		log.Error().Err(err).Str("filename", plan.Filename).Msg("export: stream aborted, archive is truncated")
	}
}
