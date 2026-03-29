package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// ── In-memory fakes ────────────────────────────────────────────────────────

type fakeUserRepo struct {
	users map[string]*model.User
	next  int64
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{users: make(map[string]*model.User)}
}

func (r *fakeUserRepo) Create(_ context.Context, username, hash string) (*model.User, error) {
	if _, ok := r.users[username]; ok {
		return nil, apperror.Conflict("duplicate")
	}
	r.next++
	u := &model.User{ID: r.next, Username: username, PasswordHash: hash, CreatedAt: time.Now()}
	r.users[username] = u
	return u, nil
}

func (r *fakeUserRepo) GetByID(_ context.Context, id int64) (*model.User, error) {
	for _, u := range r.users {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, apperror.ErrNotFound
}

func (r *fakeUserRepo) GetByUsername(_ context.Context, username string) (*model.User, error) {
	if u, ok := r.users[username]; ok {
		return u, nil
	}
	return nil, apperror.ErrNotFound
}

func (r *fakeUserRepo) Count(_ context.Context) (int, error) {
	return len(r.users), nil
}

type fakeSessionRepo struct {
	sessions map[string]*model.Session
}

func newFakeSessionRepo() *fakeSessionRepo {
	return &fakeSessionRepo{sessions: make(map[string]*model.Session)}
}

func (r *fakeSessionRepo) Create(_ context.Context, token string, userID int64, ttl int) error {
	r.sessions[token] = &model.Session{
		Token:     token,
		UserID:    userID,
		ExpiresAt: time.Now().Add(time.Duration(ttl) * time.Second),
	}
	return nil
}

func (r *fakeSessionRepo) Get(_ context.Context, token string) (*model.Session, error) {
	s, ok := r.sessions[token]
	if !ok || time.Now().After(s.ExpiresAt) {
		return nil, apperror.ErrNotFound
	}
	return s, nil
}

func (r *fakeSessionRepo) Delete(_ context.Context, token string) error {
	delete(r.sessions, token)
	return nil
}

func (r *fakeSessionRepo) DeleteExpired(_ context.Context) error {
	for k, v := range r.sessions {
		if time.Now().After(v.ExpiresAt) {
			delete(r.sessions, k)
		}
	}
	return nil
}

// ── Tests ──────────────────────────────────────────────────────────────────

func TestRegister_Success(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), true)
	u, err := svc.Register(context.Background(), "alice", "longenoughpassword123")
	require.NoError(t, err)
	assert.Equal(t, "alice", u.Username)
	assert.NotZero(t, u.ID)
}

func TestRegister_DuplicateUsername(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), true)
	_, err := svc.Register(context.Background(), "alice", "longenoughpassword123")
	require.NoError(t, err)

	_, err = svc.Register(context.Background(), "alice", "anotherlongpassword123")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

func TestRegister_ShortPassword(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), true)
	_, err := svc.Register(context.Background(), "bob", "short")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 400, appErr.Code)
}

func TestRegister_FirstUserAlwaysAllowed(t *testing.T) {
	// allowRegistration = false but first user should still register.
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), false)
	u, err := svc.Register(context.Background(), "admin", "longenoughpassword123")
	require.NoError(t, err)
	assert.Equal(t, "admin", u.Username)
}

func TestRegister_SecondUserBlockedWhenClosed(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), false)
	_, err := svc.Register(context.Background(), "admin", "longenoughpassword123")
	require.NoError(t, err)

	_, err = svc.Register(context.Background(), "hacker", "longenoughpassword123")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 403, appErr.Code)
}

func TestLogin_Success(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), true)
	_, err := svc.Register(context.Background(), "alice", "correctpassword123!")
	require.NoError(t, err)

	token, err := svc.Login(context.Background(), "alice", "correctpassword123!")
	require.NoError(t, err)
	assert.NotEmpty(t, token)
}

func TestLogin_WrongPassword(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), true)
	_, err := svc.Register(context.Background(), "alice", "correctpassword123!")
	require.NoError(t, err)

	_, err = svc.Login(context.Background(), "alice", "wrongpassword123!")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 401, appErr.Code)
}

func TestLogin_UnknownUser_SameErrorAsWrongPassword(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), true)

	_, err := svc.Login(context.Background(), "nobody", "somepassword123!")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 401, appErr.Code)
	assert.Equal(t, "invalid username or password", appErr.Message)
}

func TestLogin_ErrorMessageIsUniform(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), true)
	_, _ = svc.Register(context.Background(), "alice", "correctpassword123!")

	// Wrong user.
	_, err1 := svc.Login(context.Background(), "nobody", "somepassword123!")
	// Wrong password.
	_, err2 := svc.Login(context.Background(), "alice", "wrongpassword123!")

	var e1, e2 *apperror.AppError
	require.ErrorAs(t, err1, &e1)
	require.ErrorAs(t, err2, &e2)

	// Both must return the SAME error message (prevent username enumeration).
	assert.Equal(t, e1.Message, e2.Message)
}

func TestLogout_DeletesSession(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), true)
	_, err := svc.Register(context.Background(), "alice", "correctpassword123!")
	require.NoError(t, err)

	token, err := svc.Login(context.Background(), "alice", "correctpassword123!")
	require.NoError(t, err)

	err = svc.Logout(context.Background(), token)
	require.NoError(t, err)

	_, err = svc.GetSession(context.Background(), token)
	require.Error(t, err)
}

func TestGetSession_ReturnsActiveSession(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), true)
	_, err := svc.Register(context.Background(), "alice", "correctpassword123!")
	require.NoError(t, err)

	token, err := svc.Login(context.Background(), "alice", "correctpassword123!")
	require.NoError(t, err)

	session, err := svc.GetSession(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, token, session.Token)
}

func TestGetSession_UnknownToken(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), true)
	_, err := svc.GetSession(context.Background(), "nonexistent-token")
	require.Error(t, err)
}

func TestSessionMiddleware_Valid(t *testing.T) {
	userRepo := newFakeUserRepo()
	sessionRepo := newFakeSessionRepo()
	svc := NewService(userRepo, sessionRepo, true)

	_, err := svc.Register(context.Background(), "alice", "correctpassword123!")
	require.NoError(t, err)

	token, err := svc.Login(context.Background(), "alice", "correctpassword123!")
	require.NoError(t, err)

	var capturedUser *model.User
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := svc.SessionMiddleware(next)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, capturedUser)
	assert.Equal(t, "alice", capturedUser.Username)
}

func TestSessionMiddleware_NoCookie(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), true)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := svc.SessionMiddleware(next)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestSessionMiddleware_InvalidSession(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), true)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := svc.SessionMiddleware(next)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "invalid-token"})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestSessionMiddleware_UserNotFound(t *testing.T) {
	userRepo := newFakeUserRepo()
	sessionRepo := newFakeSessionRepo()
	svc := NewService(userRepo, sessionRepo, true)

	_, err := svc.Register(context.Background(), "alice", "correctpassword123!")
	require.NoError(t, err)

	token, err := svc.Login(context.Background(), "alice", "correctpassword123!")
	require.NoError(t, err)

	// Delete the user from the fake repo.
	delete(userRepo.users, "alice")

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := svc.SessionMiddleware(next)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestUserFromContext_WithUser(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	ctx := context.WithValue(context.Background(), userContextKey, user)
	result := UserFromContext(ctx)
	assert.Equal(t, user, result)
}

func TestUserFromContext_Missing(t *testing.T) {
	result := UserFromContext(context.Background())
	assert.Nil(t, result)
}

// ── Additional edge-case tests ──────────────────────────────────────────────

func TestRegister_LongUsername(t *testing.T) {
	svc := NewService(newFakeUserRepo(), newFakeSessionRepo(), true)
	// Username exactly 65 characters — should fail validation.
	longName := "a" + "b" + "c" + "d" + "e" + "f" + "g" + "h" + "i" + "j" +
		"a" + "b" + "c" + "d" + "e" + "f" + "g" + "h" + "i" + "j" +
		"a" + "b" + "c" + "d" + "e" + "f" + "g" + "h" + "i" + "j" +
		"a" + "b" + "c" + "d" + "e" + "f" + "g" + "h" + "i" + "j" +
		"a" + "b" + "c" + "d" + "e" + "f" + "g" + "h" + "i" + "j" +
		"a" + "b" + "c" + "d" + "e" + "f" + "g" + "h" + "i" + "j" +
		"extra"
	_, err := svc.Register(context.Background(), longName, "longenoughpassword123")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 400, appErr.Code)
}

// errCountUserRepo returns an error from Count.
type errCountUserRepo struct {
	*fakeUserRepo
}

func (r *errCountUserRepo) Count(_ context.Context) (int, error) {
	return 0, fmt.Errorf("db connection lost")
}

func TestRegister_CountError(t *testing.T) {
	repo := &errCountUserRepo{fakeUserRepo: newFakeUserRepo()}
	svc := NewService(repo, newFakeSessionRepo(), true)
	_, err := svc.Register(context.Background(), "alice", "longenoughpassword123")
	require.Error(t, err)
}

// errGetByUsernameRepo returns a non-ErrNotFound error from GetByUsername.
type errGetByUsernameRepo struct {
	*fakeUserRepo
}

func (r *errGetByUsernameRepo) GetByUsername(_ context.Context, _ string) (*model.User, error) {
	return nil, fmt.Errorf("db unavailable")
}

func TestLogin_InternalUserError(t *testing.T) {
	repo := &errGetByUsernameRepo{fakeUserRepo: newFakeUserRepo()}
	svc := NewService(repo, newFakeSessionRepo(), true)
	_, err := svc.Login(context.Background(), "alice", "somepassword123!")
	require.Error(t, err)
	// Should be an internal error, not Unauthorized.
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 500, appErr.Code)
}

// errCreateSessionRepo returns an error from Create.
type errCreateSessionRepo struct {
	*fakeSessionRepo
}

func (r *errCreateSessionRepo) Create(_ context.Context, _ string, _ int64, _ int) error {
	return fmt.Errorf("session store down")
}

func TestLogin_SessionCreateError(t *testing.T) {
	userRepo := newFakeUserRepo()
	// Insert user with a low-cost hash to avoid slow bcrypt in tests.
	hash, _ := bcrypt.GenerateFromPassword([]byte("testpassword123!"), 4)
	userRepo.next = 1
	userRepo.users["alice"] = &model.User{
		ID:           1,
		Username:     "alice",
		PasswordHash: string(hash),
	}
	sessionRepo := &errCreateSessionRepo{fakeSessionRepo: newFakeSessionRepo()}
	svc := NewService(userRepo, sessionRepo, true)

	_, err := svc.Login(context.Background(), "alice", "testpassword123!")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 500, appErr.Code)
}
