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
// Per-folder permissions are hydrated inline so the UI can render scope badges.
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
		perms, err := h.tokens.ListPermissions(c.Request.Context(), t.ID)
		if err != nil {
			writeError(c, err)
			return
		}
		t.FolderPermissions = perms
	}
	if tokens == nil {
		tokens = []*model.APIToken{}
	}
	c.JSON(http.StatusOK, tokens)
}

// CreateToken generates a new API token for the current user.
// The full token is only returned in this response — never again.
//
// Optional folder_permissions lets the caller scope the token to a set of
// folders at creation time (otherwise the global scope applies everywhere).
func (h *AccountHandler) CreateToken(c *gin.Context) {
	user := ginUser(c)

	var body struct {
		Name              string                        `json:"name"`
		Scope             string                        `json:"scope"`
		FolderPermissions []model.TokenFolderPermission `json:"folder_permissions"`
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
	// Reject folder-scoped write permissions on a read-only token — the
	// global scope is the coarse gate, folder permissions only narrow it.
	if body.Scope == "read" {
		for _, p := range body.FolderPermissions {
			if p.Permission == "write" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "cannot grant write on a read-only token; set scope=\"readwrite\" first"})
				return
			}
		}
	}
	for _, p := range body.FolderPermissions {
		if p.Permission != "read" && p.Permission != "write" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "permission must be \"read\" or \"write\""})
			return
		}
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

	if len(body.FolderPermissions) > 0 {
		if err := h.tokens.SetPermissions(c.Request.Context(), user.ID, token.ID, body.FolderPermissions); err != nil {
			// Roll back the token so the client doesn't end up with an
			// unscoped token after asking for a scoped one.
			_ = h.tokens.Delete(c.Request.Context(), user.ID, token.ID)
			writeError(c, err)
			return
		}
		token.FolderPermissions = body.FolderPermissions
	}

	// token.Token is already set from RETURNING — return it now (only time).
	c.JSON(http.StatusCreated, token)
}

// UpdateTokenPermissions replaces the folder permissions for a token and,
// optionally, its global scope. Passing an empty folder_permissions array
// clears all per-folder permissions and reverts the token to global-scope
// behavior. If scope is empty, the existing scope is left unchanged.
func (h *AccountHandler) UpdateTokenPermissions(c *gin.Context) {
	user := ginUser(c)
	tokenID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token id"})
		return
	}

	var body struct {
		Scope             string                        `json:"scope"`
		FolderPermissions []model.TokenFolderPermission `json:"folder_permissions"`
	}
	if err := readJSON(c, &body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if body.Scope != "" && body.Scope != "read" && body.Scope != "readwrite" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scope must be \"read\" or \"readwrite\""})
		return
	}
	for _, p := range body.FolderPermissions {
		if p.Permission != "read" && p.Permission != "write" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "permission must be \"read\" or \"write\""})
			return
		}
	}
	// Mirror the create-time rule: a read-only token must not carry any
	// folder-level write grants. Downgrading scope with stale write grants
	// still on file would silently over-grant at enforcement time.
	if body.Scope == "read" {
		for _, p := range body.FolderPermissions {
			if p.Permission == "write" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "cannot grant write on a read-only token; set scope=\"readwrite\" first"})
				return
			}
		}
	}

	if body.Scope != "" {
		if err := h.tokens.SetScope(c.Request.Context(), user.ID, tokenID, body.Scope); err != nil {
			writeError(c, err)
			return
		}
	}

	if err := h.tokens.SetPermissions(c.Request.Context(), user.ID, tokenID, body.FolderPermissions); err != nil {
		writeError(c, err)
		return
	}

	perms, err := h.tokens.ListPermissions(c.Request.Context(), tokenID)
	if err != nil {
		writeError(c, err)
		return
	}
	resp := gin.H{"folder_permissions": perms}
	if body.Scope != "" {
		resp["scope"] = body.Scope
	}
	c.JSON(http.StatusOK, resp)
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
