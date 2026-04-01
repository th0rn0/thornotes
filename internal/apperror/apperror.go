package apperror

import (
	"errors"
	"fmt"
	"net/http"
)

// AppError is an error that carries an HTTP status code.
type AppError struct {
	Code    int
	Message string
	Err     error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *AppError) Unwrap() error { return e.Err }

// Sentinel errors for domain logic.
var (
	ErrNotFound      = errors.New("not found")
	ErrConflict      = errors.New("conflict")
	ErrUnauthorized  = errors.New("unauthorized")
	ErrForbidden     = errors.New("forbidden")
	ErrPathTraversal = errors.New("path traversal detected")
	ErrTooLarge      = errors.New("content too large")
	ErrDiskFull      = errors.New("disk full")
)

func New(code int, message string, err error) *AppError {
	return &AppError{Code: code, Message: message, Err: err}
}

func NotFound(msg string) *AppError {
	return &AppError{Code: http.StatusNotFound, Message: msg, Err: ErrNotFound}
}

func Conflict(msg string) *AppError {
	return &AppError{Code: http.StatusConflict, Message: msg, Err: ErrConflict}
}

func Unauthorized(msg string) *AppError {
	return &AppError{Code: http.StatusUnauthorized, Message: msg, Err: ErrUnauthorized}
}

func Forbidden(msg string) *AppError {
	return &AppError{Code: http.StatusForbidden, Message: msg, Err: ErrForbidden}
}

func BadRequest(msg string) *AppError {
	return &AppError{Code: http.StatusBadRequest, Message: msg}
}

func Internal(msg string, err error) *AppError {
	return &AppError{Code: http.StatusInternalServerError, Message: msg, Err: err}
}

func DiskFull() *AppError {
	return &AppError{Code: http.StatusInsufficientStorage, Message: "disk full", Err: ErrDiskFull}
}
