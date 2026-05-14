package handler

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

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

// UpdateTokenPermissions applies partial edits to an API token: folder
// permissions, global scope, and/or display name. Any field left out of the
// request body is preserved. Passing an empty folder_permissions array clears
// all per-folder permissions and reverts the token to global-scope behavior.
// The route name is historic — it now edits any token metadata the owner is
// allowed to change, not just permissions.
func (h *AccountHandler) UpdateTokenPermissions(c *gin.Context) {
	user := ginUser(c)
	tokenID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token id"})
		return
	}

	// json.RawMessage on FolderPermissions so we can tell "omitted" apart
	// from "explicitly empty" (the latter is a request to clear the list).
	var body struct {
		Name              *string                        `json:"name"`
		Scope             string                         `json:"scope"`
		FolderPermissions *[]model.TokenFolderPermission `json:"folder_permissions"`
	}
	if err := readJSON(c, &body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must not be empty"})
		return
	}
	if body.Scope != "" && body.Scope != "read" && body.Scope != "readwrite" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scope must be \"read\" or \"readwrite\""})
		return
	}
	if body.FolderPermissions != nil {
		for _, p := range *body.FolderPermissions {
			if p.Permission != "read" && p.Permission != "write" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "permission must be \"read\" or \"write\""})
				return
			}
		}
	}

	// Enforce the create-time invariant across the full state, not just the
	// fields in this request body: after the update completes, a token with
	// scope="read" must have zero folder-level write grants. Two leak paths
	// were possible before this check:
	//
	//   1. {"scope":"read"} with no folder_permissions field — old write
	//      grants survive the scope change.
	//   2. {"folder_permissions":[{permission:"write"}]} with no scope field
	//      — write grant is added to an already read-scoped token.
	//
	// Both reduce to "what's the effective end state?", so we compute it
	// here. ListByUser also doubles as the ownership check for tokenID
	// (so the ListPermissions read below cannot leak across users).
	userTokens, err := h.tokens.ListByUser(c.Request.Context(), user.ID)
	if err != nil {
		writeError(c, err)
		return
	}
	var current *model.APIToken
	for _, t := range userTokens {
		if t.ID == tokenID {
			current = t
			break
		}
	}
	if current == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "token not found"})
		return
	}
	effectiveScope := body.Scope
	if effectiveScope == "" {
		effectiveScope = current.Scope
	}
	if effectiveScope == "read" {
		var effectivePerms []model.TokenFolderPermission
		if body.FolderPermissions != nil {
			effectivePerms = *body.FolderPermissions
		} else {
			existing, err := h.tokens.ListPermissions(c.Request.Context(), tokenID)
			if err != nil {
				writeError(c, err)
				return
			}
			effectivePerms = existing
		}
		for _, p := range effectivePerms {
			if p.Permission == "write" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "cannot grant write on a read-only token; set scope=\"readwrite\" first or remove write grants"})
				return
			}
		}
	}

	// The check above is best-effort: two concurrent PUTs from the same
	// user (e.g. two browser tabs, two desktop clients, or a script holding
	// a session cookie) can each pass the effective-state check on
	// overlapping reads and then both write, leaving the row in
	// {scope=read, write-grant=*}. That state has no exploit surface today
	// because the MCP write-tool gate at handler/mcp.go:556 reads
	// user.TokenScope per request and blocks every write tool when
	// scope=="read" regardless of folder grants — that gate is the
	// authoritative scope check. Defense-in-depth only. If a future
	// caller starts reading folder permissions without re-checking
	// TokenScope, push the invariant into the repo as a transactional
	// check on SetScope / SetPermissions. The ListPermissions read at
	// line 202 is similarly unscoped at the SQL level (see
	// repository/sqlite/api_tokens.go:163) and is only safe today because
	// the linear-scan ownership check above runs first; pushing the
	// invariant repo-side should also add a user_id filter there to
	// remove the implicit handler-side coupling.
	if body.Name != nil {
		if err := h.tokens.SetName(c.Request.Context(), user.ID, tokenID, strings.TrimSpace(*body.Name)); err != nil {
			writeError(c, err)
			return
		}
	}

	if body.Scope != "" {
		if err := h.tokens.SetScope(c.Request.Context(), user.ID, tokenID, body.Scope); err != nil {
			writeError(c, err)
			return
		}
	}

	if body.FolderPermissions != nil {
		if err := h.tokens.SetPermissions(c.Request.Context(), user.ID, tokenID, *body.FolderPermissions); err != nil {
			writeError(c, err)
			return
		}
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
	if body.Name != nil {
		resp["name"] = strings.TrimSpace(*body.Name)
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
