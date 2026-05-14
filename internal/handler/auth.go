package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/security"
)

type AuthHandler struct {
	svc           *auth.Service
	notesSvc      *notes.Service
	secureCookies bool
}

func NewAuthHandler(svc *auth.Service, notesSvc *notes.Service, secureCookies bool) *AuthHandler {
	return &AuthHandler{svc: svc, notesSvc: notesSvc, secureCookies: secureCookies}
}

type registerRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Config is an unauthenticated endpoint the SPA reads on init to decide
// whether the "Create account" affordance should be rendered. Mirrors the
// route-gate predicate in Register exactly so the UI and the route never
// disagree: if this returns false the register form is removed from the DOM
// AND Register short-circuits with 404 for any direct caller.
func (h *AuthHandler) Config(c *gin.Context) {
	open, err := h.svc.RegistrationOpen(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"allow_registration": open})
}

func (h *AuthHandler) Register(c *gin.Context) {
	// Closed-instance gate. Short-circuit before any request parsing so a
	// closed instance presents zero attack surface here — no body read, no
	// validation, no rate-limit-detectable side effects beyond what the
	// rateMW middleware already imposes. 404 matches the "page can't even
	// be accessed" UX contract, identical to what the user sees when they
	// navigate to a route that doesn't exist.
	open, err := h.svc.RegistrationOpen(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	if !open {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	var req registerRequest
	if err := readJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user, err := h.svc.Register(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		writeError(c, err)
		return
	}

	h.notesSvc.CreateGettingStartedNote(c.Request.Context(), user.ID, user.UUID)

	c.JSON(http.StatusCreated, gin.H{
		"id":         user.ID,
		"username":   user.Username,
		"created_at": user.CreatedAt,
	})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := readJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token, err := h.svc.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		writeError(c, err)
		return
	}

	// Generate CSRF token for this session.
	csrfToken, err := security.GenerateCSRFToken(token)
	if err != nil {
		writeError(c, err)
		return
	}

	maxAge := 7 * 24 * 60 * 60
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("session", token, maxAge, "/", "", h.secureCookies, true)

	c.JSON(http.StatusOK, gin.H{"csrf_token": csrfToken})
}

func (h *AuthHandler) Logout(c *gin.Context) {
	cookie, err := c.Cookie("session")
	if err == nil {
		_ = h.svc.Logout(c.Request.Context(), cookie)
		security.InvalidateCSRFToken(cookie)
	}

	c.SetCookie("session", "", -1, "/", "", false, true)

	c.JSON(http.StatusOK, gin.H{"message": "logged out"})
}

func (h *AuthHandler) Me(c *gin.Context) {
	user := auth.UserFromContext(c.Request.Context())
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":       user.ID,
		"username": user.Username,
	})
}

func (h *AuthHandler) CSRF(c *gin.Context) {
	cookie, err := c.Cookie("session")
	if err != nil || cookie == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	token, err := security.GenerateCSRFToken(cookie)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"csrf_token": token})
}
