package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// ── fakeAPITokenRepo ──────────────────────────────────────────────────────────

type fakeAPITokenRepo struct {
	tokens []*model.APIToken
	byRaw  map[string]*model.APIToken                // raw token → stored token
	perms  map[int64][]model.TokenFolderPermission   // token ID → permissions
	nextID int64
	err    error // if set, all mutating calls return this
}

func newFakeAPITokenRepo() *fakeAPITokenRepo {
	return &fakeAPITokenRepo{
		byRaw: make(map[string]*model.APIToken),
		perms: make(map[int64][]model.TokenFolderPermission),
	}
}

func (r *fakeAPITokenRepo) Create(_ context.Context, userID int64, name, token, scope string) (*model.APIToken, error) {
	if r.err != nil {
		return nil, r.err
	}
	r.nextID++
	prefix := token
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	t := &model.APIToken{
		ID:        r.nextID,
		UserID:    userID,
		Name:      name,
		Token:     token,
		Prefix:    prefix,
		Scope:     scope,
		CreatedAt: time.Now(),
	}
	r.tokens = append(r.tokens, t)
	r.byRaw[token] = t
	return t, nil
}

func (r *fakeAPITokenRepo) GetByToken(_ context.Context, token string) (*model.APIToken, error) {
	if t, ok := r.byRaw[token]; ok {
		return t, nil
	}
	return nil, apperror.ErrNotFound
}

func (r *fakeAPITokenRepo) ListByUser(_ context.Context, userID int64) ([]*model.APIToken, error) {
	if r.err != nil {
		return nil, r.err
	}
	var out []*model.APIToken
	for _, t := range r.tokens {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (r *fakeAPITokenRepo) Delete(_ context.Context, userID, tokenID int64) error {
	if r.err != nil {
		return r.err
	}
	for i, t := range r.tokens {
		if t.ID == tokenID && t.UserID == userID {
			r.tokens = append(r.tokens[:i], r.tokens[i+1:]...)
			return nil
		}
	}
	return apperror.NotFound("token not found")
}

func (r *fakeAPITokenRepo) TouchLastUsed(_ context.Context, _ int64) error { return nil }

func (r *fakeAPITokenRepo) ListPermissions(_ context.Context, tokenID int64) ([]model.TokenFolderPermission, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]model.TokenFolderPermission(nil), r.perms[tokenID]...), nil
}

func (r *fakeAPITokenRepo) SetPermissions(_ context.Context, userID, tokenID int64, perms []model.TokenFolderPermission) error {
	if r.err != nil {
		return r.err
	}
	for _, t := range r.tokens {
		if t.ID == tokenID && t.UserID == userID {
			if r.perms == nil {
				r.perms = make(map[int64][]model.TokenFolderPermission)
			}
			r.perms[tokenID] = append([]model.TokenFolderPermission(nil), perms...)
			return nil
		}
	}
	return apperror.NotFound("token not found")
}

// ── helpers ───────────────────────────────────────────────────────────────────

// newAccountRouter builds a gin router with the account handler wired up
// and an authenticated user injected via middleware.
func newAccountRouter(user *model.User, repo *fakeAPITokenRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewAccountHandler(repo)

	r.Use(func(c *gin.Context) {
		c.Set("user", user)
		c.Next()
	})

	r.GET("/tokens", h.ListTokens)
	r.POST("/tokens", h.CreateToken)
	r.DELETE("/tokens/:id", h.DeleteToken)
	r.PUT("/tokens/:id/permissions", h.UpdateTokenPermissions)
	return r
}

// ── ListTokens ────────────────────────────────────────────────────────────────

func TestListTokens_Empty(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	r := newAccountRouter(user, newFakeAPITokenRepo())

	req := httptest.NewRequest(http.MethodGet, "/tokens", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var body []interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Len(t, body, 0)
}

func TestListTokens_WithTokens(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	_, err := repo.Create(context.Background(), 1, "CI", "tn_tokenvalue123", "readwrite")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	req := httptest.NewRequest(http.MethodGet, "/tokens", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var body []map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Len(t, body, 1)
	assert.Equal(t, "CI", body[0]["name"])
	// Token must be masked — not returned in list response.
	assert.Empty(t, body[0]["token"])
}

func TestListTokens_RepoError(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	repo.err = fmt.Errorf("db unavailable")

	r := newAccountRouter(user, repo)
	req := httptest.NewRequest(http.MethodGet, "/tokens", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// ── CreateToken ───────────────────────────────────────────────────────────────

func TestCreateToken_WithName(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	r := newAccountRouter(user, newFakeAPITokenRepo())

	body := strings.NewReader(`{"name":"my token"}`)
	req := httptest.NewRequest(http.MethodPost, "/tokens", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "my token", resp["name"])
	assert.NotEmpty(t, resp["token"])
}

func TestCreateToken_DefaultName(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	r := newAccountRouter(user, newFakeAPITokenRepo())

	// Empty name → should default to "Default".
	body := strings.NewReader(`{"name":""}`)
	req := httptest.NewRequest(http.MethodPost, "/tokens", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "Default", resp["name"])
}

func TestCreateToken_InvalidJSON(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	r := newAccountRouter(user, newFakeAPITokenRepo())

	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader("notjson"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCreateToken_RepoError(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	repo.err = apperror.Internal("db error", fmt.Errorf("write failed"))

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"name":"ci"}`)
	req := httptest.NewRequest(http.MethodPost, "/tokens", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// ── DeleteToken ───────────────────────────────────────────────────────────────

func TestDeleteToken_Success(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "CI", "tn_deletetest123", "readwrite")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/tokens/%d", tok.ID), nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestDeleteToken_InvalidID(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	r := newAccountRouter(user, newFakeAPITokenRepo())

	req := httptest.NewRequest(http.MethodDelete, "/tokens/notanumber", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestDeleteToken_NotFound(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	r := newAccountRouter(user, newFakeAPITokenRepo())

	req := httptest.NewRequest(http.MethodDelete, "/tokens/999", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// ── Folder permissions ────────────────────────────────────────────────────────

func TestCreateToken_WithFolderPermissions(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	r := newAccountRouter(user, repo)

	// Note: fake repo's SetPermissions does not validate folder ownership —
	// this test only checks that the handler plumbs the permissions through.
	body := strings.NewReader(`{"name":"scoped","scope":"readwrite","folder_permissions":[{"folder_id":7,"permission":"write"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/tokens", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "scoped", resp["name"])

	perms := resp["folder_permissions"].([]interface{})
	require.Len(t, perms, 1)
}

func TestCreateToken_ReadScopeRejectsWritePermission(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	r := newAccountRouter(user, repo)

	body := strings.NewReader(`{"name":"x","scope":"read","folder_permissions":[{"folder_id":7,"permission":"write"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/tokens", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCreateToken_InvalidPermissionString(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	r := newAccountRouter(user, repo)

	body := strings.NewReader(`{"name":"x","scope":"readwrite","folder_permissions":[{"folder_id":7,"permission":"owner"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/tokens", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUpdateTokenPermissions_Success(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "t", "tn_permstest", "readwrite")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"folder_permissions":[{"folder_id":null,"permission":"read"}]}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	// Confirm the fake repo was actually mutated.
	perms, err := repo.ListPermissions(context.Background(), tok.ID)
	require.NoError(t, err)
	require.Len(t, perms, 1)
	assert.Nil(t, perms[0].FolderID)
	assert.Equal(t, "read", perms[0].Permission)
}

func TestUpdateTokenPermissions_ClearsWhenEmpty(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "t", "tn_clearperms", "readwrite")
	require.NoError(t, err)
	// Seed one permission.
	_ = repo.perms
	require.NoError(t, repo.SetPermissions(context.Background(), 1, tok.ID, []model.TokenFolderPermission{
		{FolderID: nil, Permission: "read"},
	}))

	r := newAccountRouter(user, repo)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), strings.NewReader(`{"folder_permissions":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	perms, err := repo.ListPermissions(context.Background(), tok.ID)
	require.NoError(t, err)
	assert.Empty(t, perms)
}

func TestUpdateTokenPermissions_InvalidPermission(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "t", "tn_badperm", "readwrite")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"folder_permissions":[{"folder_id":null,"permission":"admin"}]}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestDeleteToken_RepoError(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "CI", "tn_deleteerr", "readwrite")
	require.NoError(t, err)
	repo.err = apperror.Internal("db error", fmt.Errorf("write failed"))

	r := newAccountRouter(user, repo)
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/tokens/%d", tok.ID), nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}
