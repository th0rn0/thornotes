package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/th0rn0/thornotes/internal/notes"
)

type JournalsHandler struct {
	svc *notes.Service
}

func NewJournalsHandler(svc *notes.Service) *JournalsHandler {
	return &JournalsHandler{svc: svc}
}

type createJournalRequest struct {
	Name string `json:"name"`
}

func (h *JournalsHandler) List(c *gin.Context) {
	user := ginUser(c)
	journals, err := h.svc.ListJournals(c.Request.Context(), user.ID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, journals)
}

func (h *JournalsHandler) Create(c *gin.Context) {
	user := ginUser(c)
	var req createJournalRequest
	if err := readJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	journal, err := h.svc.CreateJournal(c.Request.Context(), user.ID, user.UUID, req.Name)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, journal)
}

func (h *JournalsHandler) Delete(c *gin.Context) {
	user := ginUser(c)
	id, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := h.svc.DeleteJournal(c.Request.Context(), user.ID, id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *JournalsHandler) Today(c *gin.Context) {
	user := ginUser(c)
	id, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	loc := time.UTC
	if tz := c.Query("tz"); tz != "" {
		parsed, err := time.LoadLocation(tz)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unrecognised timezone %q — use an IANA timezone name, e.g. \"America/New_York\", \"Europe/London\", or \"Asia/Tokyo\"", tz)})
			return
		}
		loc = parsed
	}

	note, err := h.svc.TodayEntry(c.Request.Context(), user.ID, user.UUID, id, loc)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, note)
}
