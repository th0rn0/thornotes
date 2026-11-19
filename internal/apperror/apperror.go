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
	// ErrShareExpired is the refusal of a write that would revive a share link
	// whose deadline has already passed. It is a sentinel rather than a string
	// so the layers agree: the repository returns it, internal/notes turns it
	// into the explanation a caller can act on, and the handler renders 409.
	//
	// It is deliberately NOT ErrConflict: a concurrent edit and a dead link are
	// different answers to the caller, and one of them has a way forward
	// (re-share, which rotates the token).
	ErrShareExpired = errors.New("share link expired")
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

// ShareExpired returns the 409 a caller gets when a write would revive a share
// link whose deadline has passed.
//
// 409 and not 400: the request is well-formed and the caller is entitled to make
// it — what refuses it is the current state of the resource, which is what 409
// means. 404 would be wrong in the other direction: this route is authenticated
// and owner-scoped, so the uniform-404 rule that hides the existence of a link on
// the anonymous surface does not apply, and the caller owns the link.
func ShareExpired(msg string) *AppError {
	return &AppError{Code: http.StatusConflict, Message: msg, Err: ErrShareExpired}
}

// IsShareExpired reports whether err is that refusal.
func IsShareExpired(err error) bool {
	return errors.Is(err, ErrShareExpired)
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

func NotImplemented(msg string) *AppError {
	return &AppError{Code: http.StatusNotImplemented, Message: msg}
}

// IsConflict reports whether err is a 409 Conflict AppError.
func IsConflict(err error) bool {
	var appErr *AppError
	return errors.As(err, &appErr) && appErr.Code == http.StatusConflict
}
