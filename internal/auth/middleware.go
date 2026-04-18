package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
)

type contextKey string

const (
	userContextKey  contextKey = "user"
	authzContextKey contextKey = "authz"
)

// SessionMiddleware validates the session cookie and injects the authenticated user into context.
func (s *Service) SessionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Cookie("session")
		if err != nil || cookie == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		session, err := s.sessions.Get(c.Request.Context(), cookie)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		user, err := s.users.GetByID(c.Request.Context(), session.UserID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		// Set user both ways so auth.UserFromContext still works. Session
		// auth has no per-folder scoping, so attach a session authz marker.
		ctx := context.WithValue(c.Request.Context(), userContextKey, user)
		ctx = context.WithValue(ctx, authzContextKey, SessionAuthz())
		c.Request = c.Request.WithContext(ctx)
		c.Set("user", user)
		c.Next()
	}
}

// UserFromContext retrieves the authenticated user from the request context.
func UserFromContext(ctx context.Context) *model.User {
	u, _ := ctx.Value(userContextKey).(*model.User)
	return u
}

// AuthzFromContext returns the TokenAuthz attached by the auth middleware,
// or nil if none was attached (unauthenticated request).
func AuthzFromContext(ctx context.Context) *TokenAuthz {
	a, _ := ctx.Value(authzContextKey).(*TokenAuthz)
	return a
}

// BearerMiddleware validates an Authorization: Bearer <token> header using the
// api_tokens table and injects the authenticated user into context.
func BearerMiddleware(tokens repository.APITokenRepository, users repository.UserRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		hdr := c.Request.Header.Get("Authorization")
		if !strings.HasPrefix(hdr, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		rawToken := strings.TrimPrefix(hdr, "Bearer ")
		if rawToken == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		apiToken, err := tokens.GetByToken(c.Request.Context(), rawToken)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		user, err := users.GetByID(c.Request.Context(), apiToken.UserID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		// Load folder-level permissions; empty = token runs under global scope.
		perms, err := tokens.ListPermissions(c.Request.Context(), apiToken.ID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "authz load failed"})
			return
		}

		// Update last_used_at asynchronously — don't block the request.
		go func() {
			_ = tokens.TouchLastUsed(context.Background(), apiToken.ID)
		}()

		// Stamp the token scope onto the user so handlers can enforce it.
		user.TokenScope = apiToken.Scope

		// Set user both ways so auth.UserFromContext still works.
		ctx := context.WithValue(c.Request.Context(), userContextKey, user)
		ctx = context.WithValue(ctx, authzContextKey, NewTokenAuthz(apiToken, perms))
		c.Request = c.Request.WithContext(ctx)
		c.Set("user", user)
		c.Next()
	}
}
