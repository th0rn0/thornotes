package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// writeJSON writes v as JSON with the given status code.
func writeJSON(c *gin.Context, status int, v any) {
	c.JSON(status, v)
}

// writeError writes an AppError or generic error as a JSON error response.
func writeError(c *gin.Context, err error) {
	var appErr *apperror.AppError
	if errors.As(err, &appErr) {
		c.JSON(appErr.Code, gin.H{"error": appErr.Message})
		return
	}
	if errors.Is(err, apperror.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if errors.Is(err, apperror.ErrConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "conflict"})
		return
	}
	log.Error().Err(err).Msg("unhandled error")
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
}

// readJSON decodes the request body into v.
func readJSON(c *gin.Context, v any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ginUser retrieves the authenticated user from gin context.
func ginUser(c *gin.Context) *model.User {
	u, _ := c.Get("user")
	return u.(*model.User)
}

// ginParamInt64 extracts a named path parameter and parses it as int64.
func ginParamInt64(c *gin.Context, name string) (int64, error) {
	s := c.Param(name)
	return strconv.ParseInt(s, 10, 64)
}
