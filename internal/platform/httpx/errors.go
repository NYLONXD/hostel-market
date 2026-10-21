// Package httpx holds the HTTP plumbing shared by every module: the JSON error format,
// request decoding/validation and common middleware.
package httpx

import (
	"errors"
	"log/slog"
	"net/http"

	"hostelmarket/internal/platform/reqctx"
)

// Error is an API error. Code is stable and machine-readable; Message is for humans;
// Err is the internal cause, which is logged and never sent to the client.
type Error struct {
	Status  int
	Code    string
	Message string
	Details any
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Err.Error()
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// WithDetails returns a copy carrying structured details (e.g. field errors).
func (e *Error) WithDetails(d any) *Error {
	cp := *e
	cp.Details = d
	return &cp
}

func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func BadRequest(code, message string) *Error {
	return NewError(http.StatusBadRequest, code, message)
}

func Unauthorized(message string) *Error {
	return NewError(http.StatusUnauthorized, "unauthorized", message)
}

func Forbidden(code, message string) *Error {
	return NewError(http.StatusForbidden, code, message)
}

func NotFound(message string) *Error {
	return NewError(http.StatusNotFound, "not_found", message)
}

func Conflict(code, message string) *Error {
	return NewError(http.StatusConflict, code, message)
}

func Unavailable(code, message string) *Error {
	return NewError(http.StatusServiceUnavailable, code, message)
}

// Internal hides err from the client but keeps it for the log.
func Internal(err error) *Error {
	return &Error{Status: http.StatusInternalServerError, Code: "internal", Message: "internal server error", Err: err}
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
	Details   any    `json:"details,omitempty"`
}

// WriteError renders any error in the standard envelope. Errors that are not *Error
// become 500s.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		apiErr = Internal(err)
	}
	if apiErr.Status >= 500 {
		slog.ErrorContext(r.Context(), "request failed", "code", apiErr.Code, "err", err)
	}
	WriteJSON(w, apiErr.Status, errorEnvelope{Error: errorBody{
		Code:      apiErr.Code,
		Message:   apiErr.Message,
		RequestID: reqctx.RequestID(r.Context()),
		Details:   apiErr.Details,
	}})
}

// HandlerFunc is a handler that returns its error instead of writing it.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Handle adapts a HandlerFunc, rendering any returned error.
func Handle(h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			WriteError(w, r, err)
		}
	}
}
