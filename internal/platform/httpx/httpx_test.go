package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sampleReq struct {
	Title string `json:"title" validate:"required,min=3"`
	Price int64  `json:"price_paise" validate:"gt=0"`
}

func decode(body string) error {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	var dst sampleReq
	return DecodeJSON(w, r, &dst)
}

func TestDecodeJSON(t *testing.T) {
	require.NoError(t, decode(`{"title":"Cycle","price_paise":100}`))

	tests := map[string]struct {
		body   string
		status int
		code   string
	}{
		"empty":         {"", 400, "invalid_json"},
		"malformed":     {`{"title":`, 400, "invalid_json"},
		"wrong type":    {`{"title":"Cycle","price_paise":"cheap"}`, 400, "invalid_json"},
		"unknown field": {`{"title":"Cycle","price_paise":1,"admin":true}`, 400, "invalid_json"},
		"two objects":   {`{"title":"Cycle","price_paise":1}{}`, 400, "invalid_json"},
		"validation":    {`{"title":"ab","price_paise":0}`, 422, "validation_failed"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var apiErr *Error
			require.ErrorAs(t, decode(tc.body), &apiErr)
			assert.Equal(t, tc.status, apiErr.Status)
			assert.Equal(t, tc.code, apiErr.Code)
		})
	}
}

func TestValidationDetailsUseJSONNames(t *testing.T) {
	var apiErr *Error
	require.ErrorAs(t, decode(`{"title":"ab","price_paise":0}`), &apiErr)
	fields := apiErr.Details.([]FieldError)
	names := []string{fields[0].Field, fields[1].Field}
	assert.ElementsMatch(t, []string{"title", "price_paise"}, names)
}

func TestErrorEnvelope(t *testing.T) {
	h := RequestID(Handle(func(w http.ResponseWriter, r *http.Request) error {
		return Forbidden("not_eligible", "only Block B residents can buy this")
	}))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(RequestIDHeader, "req-123")
	h.ServeHTTP(w, r)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "req-123", w.Header().Get(RequestIDHeader))
	var body errorEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "not_eligible", body.Error.Code)
	assert.Equal(t, "only Block B residents can buy this", body.Error.Message)
	assert.Equal(t, "req-123", body.Error.RequestID)
}

func TestUnknownErrorsBecomeOpaque500(t *testing.T) {
	h := Handle(func(w http.ResponseWriter, r *http.Request) error {
		return errors.New("pq: password authentication failed for user secret")
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), "password")
	assert.Contains(t, w.Body.String(), `"code":"internal"`)
}

func TestRecoverer(t *testing.T) {
	h := Recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestRequestIDRejectsJunk(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(RequestIDHeader, "has spaces\nand newlines")
	h.ServeHTTP(w, r)
	got := w.Header().Get(RequestIDHeader)
	assert.Len(t, got, 24)
	assert.NotContains(t, got, " ")
}

func TestRedactQuery(t *testing.T) {
	assert.Equal(t, "", redactQuery(""))
	got := redactQuery("access_token=eyJhbGci&listing=42")
	assert.NotContains(t, got, "eyJhbGci")
	assert.Contains(t, got, "listing=42")
}
