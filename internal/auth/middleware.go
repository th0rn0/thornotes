package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
)

type contextKey string

const userContextKey contextKey = "user"

// SessionMiddleware validates the session cookie and injects the authenticated user into context.
func (s *Service) SessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil || cookie.Value == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		session, err := s.sessions.Get(r.Context(), cookie.Value)
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		user, err := s.users.GetByID(r.Context(), session.UserID)
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserFromContext retrieves the authenticated user from the request context.
func UserFromContext(ctx context.Context) *model.User {
	u, _ := ctx.Value(userContextKey).(*model.User)
	return u
}

// BearerMiddleware validates an Authorization: Bearer <token> header using the
// api_tokens table and injects the authenticated user into context.
func BearerMiddleware(tokens repository.APITokenRepository, users repository.UserRepository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr := r.Header.Get("Authorization")
			if !strings.HasPrefix(hdr, "Bearer ") {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			rawToken := strings.TrimPrefix(hdr, "Bearer ")
			if rawToken == "" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			apiToken, err := tokens.GetByToken(r.Context(), rawToken)
			if err != nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			user, err := users.GetByID(r.Context(), apiToken.UserID)
			if err != nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			// Update last_used_at asynchronously — don't block the request.
			go func() {
				_ = tokens.TouchLastUsed(context.Background(), apiToken.ID)
			}()

			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
