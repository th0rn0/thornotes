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

func (h *AuthHandler) Register(c *gin.Context) {
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

	h.notesSvc.CreateGettingStartedNote(c.Request.Context(), user.ID)

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
