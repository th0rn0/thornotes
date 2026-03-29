package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
