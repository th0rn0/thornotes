package handler

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/th0rn0/thornotes/internal/auth"
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
func (h *AccountHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	tokens, err := h.tokens.ListByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	// Mask the full token before sending — it was shown once on creation only.
	for _, t := range tokens {
		t.Token = ""
	}
	if tokens == nil {
		tokens = []*model.APIToken{}
	}
	writeJSON(w, http.StatusOK, tokens)
}

// CreateToken generates a new API token for the current user.
// The full token is only returned in this response — never again.
func (h *AccountHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())

	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Name == "" {
		body.Name = "Default"
	}

	raw, err := generateAPIToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not generate token"})
		return
	}

	token, err := h.tokens.Create(r.Context(), user.ID, body.Name, raw)
	if err != nil {
		writeError(w, err)
		return
	}
	// token.Token is already set from RETURNING — return it now (only time).
	writeJSON(w, http.StatusCreated, token)
}

// DeleteToken revokes an API token by ID.
func (h *AccountHandler) DeleteToken(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	idStr := r.PathValue("id")
	tokenID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid token id"})
		return
	}

	if err := h.tokens.Delete(r.Context(), user.ID, tokenID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func generateAPIToken() (string, error) {
	b := make([]byte, apiTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "tn_" + hex.EncodeToString(b), nil
}
