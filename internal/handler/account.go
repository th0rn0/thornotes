package handler

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
)

const apiTokenBytes = 32

// AccountHandler handles API token management for the /api/v1/account routes.
type AccountHandler struct {
	tokens repository.APITokenRepository
}

func NewAccountHandler(tokens repository.APITokenRepository) *AccountHandler {
	return &AccountHandler{tokens: tokens}
}

// ListTokens returns the list of API tokens for the current user.
// The full token value is never returned here — only the prefix.
func (h *AccountHandler) ListTokens(c *gin.Context) {
	user := ginUser(c)
	tokens, err := h.tokens.ListByUser(c.Request.Context(), user.ID)
	if err != nil {
		writeError(c, err)
		return
	}
	// Mask the full token before sending — it was shown once on creation only.
	for _, t := range tokens {
		t.Token = ""
	}
	if tokens == nil {
		tokens = []*model.APIToken{}
	}
	c.JSON(http.StatusOK, tokens)
}

// CreateToken generates a new API token for the current user.
// The full token is only returned in this response — never again.
func (h *AccountHandler) CreateToken(c *gin.Context) {
	user := ginUser(c)

	var body struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	if err := readJSON(c, &body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if body.Name == "" {
		body.Name = "Default"
	}
	if body.Scope == "" {
		body.Scope = "readwrite"
	}
	if body.Scope != "read" && body.Scope != "readwrite" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scope must be \"read\" or \"readwrite\""})
		return
	}

	raw, err := generateAPIToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not generate token"})
		return
	}

	token, err := h.tokens.Create(c.Request.Context(), user.ID, body.Name, raw, body.Scope)
	if err != nil {
		writeError(c, err)
		return
	}
	// token.Token is already set from RETURNING — return it now (only time).
	c.JSON(http.StatusCreated, token)
}

// DeleteToken revokes an API token by ID.
func (h *AccountHandler) DeleteToken(c *gin.Context) {
	user := ginUser(c)
	tokenID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token id"})
		return
	}

	if err := h.tokens.Delete(c.Request.Context(), user.ID, tokenID); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func generateAPIToken() (string, error) {
	b := make([]byte, apiTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "tn_" + hex.EncodeToString(b), nil
}
