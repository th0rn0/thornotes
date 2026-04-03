package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
)

const (
	bcryptCost        = 12
	sessionTokenBytes = 32
	sessionTTL        = 7 * 24 * 60 * 60 // 7 days in seconds
	minPasswordLen    = 12
)

type Service struct {
	users    repository.UserRepository
	sessions repository.SessionRepository
	// allowRegistration controls whether new users can register.
	// The very first user can always register regardless of this flag.
	allowRegistration bool
	bcryptCost        int
}

func NewService(users repository.UserRepository, sessions repository.SessionRepository, allowRegistration bool) *Service {
	return &Service{users: users, sessions: sessions, allowRegistration: allowRegistration, bcryptCost: bcryptCost}
}

// NewServiceForTest returns a Service with a reduced bcrypt cost (bcrypt.MinCost)
// so that register/login operations complete in milliseconds during tests.
func NewServiceForTest(users repository.UserRepository, sessions repository.SessionRepository, allowRegistration bool) *Service {
	return &Service{users: users, sessions: sessions, allowRegistration: allowRegistration, bcryptCost: bcrypt.MinCost}
}

func (s *Service) Register(ctx context.Context, username, password string) (*model.User, error) {
	if len(username) < 2 || len(username) > 64 {
		return nil, apperror.BadRequest("username must be 2–64 characters")
	}
	if len(password) < minPasswordLen {
		return nil, apperror.BadRequest(fmt.Sprintf("password must be at least %d characters", minPasswordLen))
	}

	// Allow first user regardless of flag.
	count, err := s.users.Count(ctx)
	if err != nil {
		return nil, apperror.Internal("count users", err)
	}
	if count > 0 && !s.allowRegistration {
		return nil, apperror.Forbidden("registration is closed")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return nil, apperror.Internal("hash password", err)
	}

	userUUID := uuid.New().String()
	user, err := s.users.Create(ctx, username, string(hash), userUUID)
	if err != nil {
		return nil, err // already an AppError from repo
	}
	return user, nil
}

// Login returns a session token on success.
// Identical error message whether user doesn't exist or password is wrong — prevents username enumeration.
func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	const genericErr = "invalid username or password"

	user, err := s.users.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, apperror.ErrNotFound) {
			// Still do a bcrypt comparison to prevent timing attacks.
			// Intentional: dummy comparison to prevent timing attacks.
			// The hash is invalid by design so this always fails — error is expected.
			_ = bcrypt.CompareHashAndPassword([]byte("$2a$12$invalidhashpadding000000000000000000000000000000000000"), []byte(password))
			return "", apperror.Unauthorized(genericErr)
		}
		return "", apperror.Internal("get user", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", apperror.Unauthorized(genericErr)
	}

	token, err := generateToken()
	if err != nil {
		return "", apperror.Internal("generate token", err)
	}

	if err := s.sessions.Create(ctx, token, user.ID, sessionTTL); err != nil {
		return "", apperror.Internal("create session", err)
	}

	return token, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	return s.sessions.Delete(ctx, token)
}

func (s *Service) GetSession(ctx context.Context, token string) (*model.Session, error) {
	return s.sessions.Get(ctx, token)
}

func generateToken() (string, error) {
	b := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
