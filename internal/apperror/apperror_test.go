package apperror

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := New(http.StatusTeapot, "I'm a teapot", sentinel)
	require.NotNil(t, e)
	assert.Equal(t, http.StatusTeapot, e.Code)
	assert.Equal(t, "I'm a teapot", e.Message)
	assert.Equal(t, sentinel, e.Err)
}

func TestNotFound(t *testing.T) {
	e := NotFound("resource not found")
	require.NotNil(t, e)
	assert.Equal(t, http.StatusNotFound, e.Code)
	assert.Equal(t, "resource not found", e.Message)
	assert.True(t, errors.Is(e, ErrNotFound))
}

func TestConflict(t *testing.T) {
	e := Conflict("already exists")
	require.NotNil(t, e)
	assert.Equal(t, http.StatusConflict, e.Code)
	assert.Equal(t, "already exists", e.Message)
	assert.True(t, errors.Is(e, ErrConflict))
}

func TestUnauthorized(t *testing.T) {
	e := Unauthorized("not authorized")
	require.NotNil(t, e)
	assert.Equal(t, http.StatusUnauthorized, e.Code)
	assert.Equal(t, "not authorized", e.Message)
	assert.True(t, errors.Is(e, ErrUnauthorized))
}

func TestForbidden(t *testing.T) {
	e := Forbidden("forbidden")
	require.NotNil(t, e)
	assert.Equal(t, http.StatusForbidden, e.Code)
	assert.Equal(t, "forbidden", e.Message)
	assert.True(t, errors.Is(e, ErrForbidden))
}

func TestBadRequest(t *testing.T) {
	e := BadRequest("bad input")
	require.NotNil(t, e)
	assert.Equal(t, http.StatusBadRequest, e.Code)
	assert.Equal(t, "bad input", e.Message)
	assert.Nil(t, e.Err)
}

func TestInternal(t *testing.T) {
	cause := errors.New("db error")
	e := Internal("internal failure", cause)
	require.NotNil(t, e)
	assert.Equal(t, http.StatusInternalServerError, e.Code)
	assert.Equal(t, "internal failure", e.Message)
	assert.Equal(t, cause, e.Err)
}

func TestError_NilErr(t *testing.T) {
	e := BadRequest("bad input")
	assert.Equal(t, "bad input", e.Error())
}

func TestError_WithErr(t *testing.T) {
	cause := errors.New("underlying cause")
	e := Internal("something failed", cause)
	assert.Equal(t, "something failed: underlying cause", e.Error())
}

func TestUnwrap(t *testing.T) {
	cause := errors.New("wrapped cause")
	e := Internal("msg", cause)
	assert.Equal(t, cause, e.Unwrap())
}

func TestUnwrap_Nil(t *testing.T) {
	e := BadRequest("no wrap")
	assert.Nil(t, e.Unwrap())
}

func TestErrors_Is_Sentinel(t *testing.T) {
	e := NotFound("not found")
	assert.True(t, errors.Is(e, ErrNotFound))
	assert.False(t, errors.Is(e, ErrConflict))
}
