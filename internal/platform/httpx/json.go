package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxBodyBytes = 1 << 20

// WriteJSON writes v with the given status. A nil v writes only the status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	if v == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// DecodeJSON reads exactly one JSON object into dst (unknown fields rejected, 1 MiB cap)
// and then validates it.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return BadRequest("invalid_json", "request body must contain a single JSON object")
	}
	return Validate(dst)
}

func decodeError(err error) error {
	var (
		syntaxErr *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
		maxErr    *http.MaxBytesError
		timeErr   *time.ParseError
	)
	switch {
	case errors.As(err, &timeErr):
		return BadRequest("invalid_json", "invalid timestamp; use RFC 3339, e.g. 2026-10-21T18:30:00+05:30")
	case errors.Is(err, io.EOF):
		return BadRequest("invalid_json", "request body is empty")
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		return BadRequest("invalid_json", "request body is not valid JSON")
	case errors.As(err, &typeErr):
		return BadRequest("invalid_json", fmt.Sprintf("field %q has the wrong type", typeErr.Field))
	case errors.As(err, &maxErr):
		return NewError(http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large")
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return BadRequest("invalid_json", "unknown field "+strings.TrimPrefix(err.Error(), "json: unknown field "))
	default:
		return BadRequest("invalid_json", "request body could not be decoded")
	}
}
