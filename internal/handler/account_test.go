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
	byRaw  map[string]*model.APIToken              // raw token → stored token
	perms  map[int64][]model.TokenFolderPermission // token ID → permissions
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

func (r *fakeAPITokenRepo) SetScope(_ context.Context, userID, tokenID int64, scope string) error {
	if r.err != nil {
		return r.err
	}
	for _, t := range r.tokens {
		if t.ID == tokenID && t.UserID == userID {
			t.Scope = scope
			return nil
		}
	}
	return apperror.NotFound("token not found")
}

func (r *fakeAPITokenRepo) SetName(_ context.Context, userID, tokenID int64, name string) error {
	if r.err != nil {
		return r.err
	}
	for _, t := range r.tokens {
		if t.ID == tokenID && t.UserID == userID {
			t.Name = name
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

func TestUpdateTokenPermissions_ChangesScope(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "t", "tn_scopechange", "readwrite")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"scope":"read","folder_permissions":[]}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	// The stored token should have the new scope.
	updated, err := repo.GetByToken(context.Background(), "tn_scopechange")
	require.NoError(t, err)
	assert.Equal(t, "read", updated.Scope)
}

func TestUpdateTokenPermissions_RejectsInvalidScope(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "t", "tn_badscope", "readwrite")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"scope":"owner","folder_permissions":[]}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUpdateTokenPermissions_ReadScopeRejectsWriteFolderGrant(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "t", "tn_downgrade", "readwrite")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"scope":"read","folder_permissions":[{"folder_id":9,"permission":"write"}]}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUpdateTokenPermissions_OmittedScopeKeepsExisting(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "t", "tn_keepscope", "read")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"folder_permissions":[]}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	kept, err := repo.GetByToken(context.Background(), "tn_keepscope")
	require.NoError(t, err)
	assert.Equal(t, "read", kept.Scope)
}

func TestUpdateTokenPermissions_ChangesName(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "Old Name", "tn_renamed", "readwrite")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"name":"Claude Desktop"}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	renamed, err := repo.GetByToken(context.Background(), "tn_renamed")
	require.NoError(t, err)
	assert.Equal(t, "Claude Desktop", renamed.Name)
}

// Regression: submitting the edit modal without changing anything used to
// return 404 on MySQL (RowsAffected=0 for no-op UPDATEs) and could hit the
// same trap on SQLite for same-value updates. The repo layer now verifies
// existence after a zero-row UPDATE before declaring the token missing.
func TestUpdateTokenPermissions_SameValueIsNotNotFound(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "Pinned", "tn_noop", "read")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	// Every field matches the current state — classic "open edit modal,
	// click Save without touching anything."
	body := strings.NewReader(`{"name":"Pinned","scope":"read","folder_permissions":[]}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equalf(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	kept, err := repo.GetByToken(context.Background(), "tn_noop")
	require.NoError(t, err)
	assert.Equal(t, "Pinned", kept.Name)
	assert.Equal(t, "read", kept.Scope)
}

func TestUpdateTokenPermissions_EmptyNameRejected(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "Original", "tn_emptyname", "readwrite")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"name":"   "}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	// The name must not have been mutated.
	kept, err := repo.GetByToken(context.Background(), "tn_emptyname")
	require.NoError(t, err)
	assert.Equal(t, "Original", kept.Name)
}

func TestUpdateTokenPermissions_NameIsTrimmed(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "Original", "tn_trimname", "readwrite")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"name":"  Renamed  "}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	kept, err := repo.GetByToken(context.Background(), "tn_trimname")
	require.NoError(t, err)
	assert.Equal(t, "Renamed", kept.Name)
}

func TestUpdateTokenPermissions_OmittedFieldsKeepExisting(t *testing.T) {
	// Sending only "name" must preserve scope AND existing folder permissions.
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "Original", "tn_partial", "read")
	require.NoError(t, err)
	// Seed one permission so we can assert it survives.
	require.NoError(t, repo.SetPermissions(context.Background(), 1, tok.ID, []model.TokenFolderPermission{
		{FolderID: nil, Permission: "read"},
	}))

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"name":"Only Rename"}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	kept, err := repo.GetByToken(context.Background(), "tn_partial")
	require.NoError(t, err)
	assert.Equal(t, "Only Rename", kept.Name)
	assert.Equal(t, "read", kept.Scope, "scope must be preserved when not in body")
	perms, err := repo.ListPermissions(context.Background(), tok.ID)
	require.NoError(t, err)
	require.Len(t, perms, 1, "folder_permissions must be preserved when key is omitted from body")
	assert.Equal(t, "read", perms[0].Permission)
}

func TestUpdateTokenPermissions_CombinedEdit(t *testing.T) {
	// All three fields at once: rename, upgrade scope, set a folder grant.
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "Original", "tn_combined", "read")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"name":"Combined","scope":"readwrite","folder_permissions":[{"folder_id":null,"permission":"write"}]}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	kept, err := repo.GetByToken(context.Background(), "tn_combined")
	require.NoError(t, err)
	assert.Equal(t, "Combined", kept.Name)
	assert.Equal(t, "readwrite", kept.Scope)
	perms, err := repo.ListPermissions(context.Background(), tok.ID)
	require.NoError(t, err)
	require.Len(t, perms, 1)
	assert.Equal(t, "write", perms[0].Permission)
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

// Regression: a scope-only downgrade to "read" used to leave stale folder-level
// write grants behind. The global MCP write-tool gate catches the actual
// exploit attempt today, but the data-integrity contract still requires the
// invariant to hold — and a future code path that consults CanWrite without
// re-checking TokenScope would be exploitable.
func TestUpdateTokenPermissions_ScopeOnlyDowngrade_RefusedWithStaleWriteGrants(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "t", "tn_stalewrite", "readwrite")
	require.NoError(t, err)
	folderID := int64(9)
	require.NoError(t, repo.SetPermissions(context.Background(), 1, tok.ID, []model.TokenFolderPermission{
		{FolderID: &folderID, Permission: "write"},
	}))

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"scope":"read"}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	// The token must NOT have been downgraded — the rejection happens before
	// any mutation runs.
	kept, err := repo.GetByToken(context.Background(), "tn_stalewrite")
	require.NoError(t, err)
	assert.Equal(t, "readwrite", kept.Scope)
}

// Regression: an already read-scoped token used to accept new folder-level
// write grants when the request body omitted the "scope" field (the old
// validation only fired when body.Scope == "read"). Effective state must
// be enforced, not just the fields supplied this call.
func TestUpdateTokenPermissions_AddWriteGrant_RefusedOnReadScopedToken(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "t", "tn_addwrite", "read")
	require.NoError(t, err)

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"folder_permissions":[{"folder_id":9,"permission":"write"}]}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	// No permissions should have been written.
	perms, err := repo.ListPermissions(context.Background(), tok.ID)
	require.NoError(t, err)
	assert.Empty(t, perms)
}

// A scope downgrade to "read" with only read grants in place is fine — the
// invariant is about write grants, not folder-level grants in general.
func TestUpdateTokenPermissions_ScopeOnlyDowngrade_AllowedWithReadGrants(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "t", "tn_readgrants", "readwrite")
	require.NoError(t, err)
	folderID := int64(9)
	require.NoError(t, repo.SetPermissions(context.Background(), 1, tok.ID, []model.TokenFolderPermission{
		{FolderID: &folderID, Permission: "read"},
	}))

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"scope":"read"}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	downgraded, err := repo.GetByToken(context.Background(), "tn_readgrants")
	require.NoError(t, err)
	assert.Equal(t, "read", downgraded.Scope)
	perms, err := repo.ListPermissions(context.Background(), tok.ID)
	require.NoError(t, err)
	assert.Len(t, perms, 1) // read grant preserved
}

// A combined downgrade + perm replacement is a single coherent intent and must
// succeed: clear the write grants by supplying a new perms list along with
// scope=read.
func TestUpdateTokenPermissions_CombinedDowngradeAndClearWriteGrants(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	tok, err := repo.Create(context.Background(), 1, "t", "tn_combined", "readwrite")
	require.NoError(t, err)
	folderID := int64(9)
	require.NoError(t, repo.SetPermissions(context.Background(), 1, tok.ID, []model.TokenFolderPermission{
		{FolderID: &folderID, Permission: "write"},
	}))

	r := newAccountRouter(user, repo)
	body := strings.NewReader(`{"scope":"read","folder_permissions":[]}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", tok.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	out, err := repo.GetByToken(context.Background(), "tn_combined")
	require.NoError(t, err)
	assert.Equal(t, "read", out.Scope)
	perms, err := repo.ListPermissions(context.Background(), tok.ID)
	require.NoError(t, err)
	assert.Empty(t, perms)
}

// Updating a token ID the caller does not own returns 404 — this is the
// ownership-check leg of the new effective-state validation. Without this,
// the new ListPermissions lookup could become a write-grant probe oracle
// for any tokenID.
func TestUpdateTokenPermissions_NotOwnedReturns404(t *testing.T) {
	alice := &model.User{ID: 1, Username: "alice"}
	repo := newFakeAPITokenRepo()
	// Bob's token — not visible to Alice. Seed with values that DIFFER from
	// the PUT body below so the post-call assertions can actually distinguish
	// "404 short-circuited cleanly" from "404 reported but mutation ran
	// anyway". Bob also gets a folder grant so the perms check below can
	// observe a survived row.
	bobToken, err := repo.Create(context.Background(), 2, "bob-original-name", "tn_bobs", "readwrite")
	require.NoError(t, err)
	bobFolderID := int64(77)
	require.NoError(t, repo.SetPermissions(context.Background(), 2, bobToken.ID, []model.TokenFolderPermission{
		{FolderID: &bobFolderID, Permission: "write"},
	}))

	r := newAccountRouter(alice, repo)
	body := strings.NewReader(`{"name":"hijacked","scope":"read","folder_permissions":[]}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tokens/%d/permissions", bobToken.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	// The 404 must short-circuit before any mutation. Confirm Bob's token
	// kept its original name, scope, and the seeded write grant — a future
	// regression that returns 404 while still calling SetName / SetScope /
	// SetPermissions would slip through a status-code-only assertion.
	after, err := repo.GetByToken(context.Background(), "tn_bobs")
	require.NoError(t, err)
	assert.Equal(t, "bob-original-name", after.Name, "alice must not be able to rename bob's token")
	assert.Equal(t, "readwrite", after.Scope, "alice must not be able to downgrade bob's token")
	perms, err := repo.ListPermissions(context.Background(), bobToken.ID)
	require.NoError(t, err)
	require.Len(t, perms, 1, "alice must not be able to clear bob's permissions")
	assert.Equal(t, "write", perms[0].Permission)
	require.NotNil(t, perms[0].FolderID)
	assert.Equal(t, bobFolderID, *perms[0].FolderID)
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
