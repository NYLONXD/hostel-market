package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hostelmarket/internal/config"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/reqctx"
	"hostelmarket/internal/users"
)

func testJWTConfig() config.JWTConfig {
	return config.JWTConfig{
		AccessSecret:  []byte(strings.Repeat("a", 32)),
		RefreshSecret: []byte(strings.Repeat("r", 32)),
		AccessTTL:     15 * time.Minute,
		RefreshTTL:    24 * time.Hour,
		Issuer:        "hostelmarket-test",
	}
}

type memBlocklist struct {
	mu      sync.Mutex
	blocked map[string]bool
	err     error
}

func (m *memBlocklist) Block(_ context.Context, jti string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.blocked == nil {
		m.blocked = map[string]bool{}
	}
	m.blocked[jti] = true
	return nil
}

func (m *memBlocklist) IsBlocked(_ context.Context, jti string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.blocked[jti], m.err
}

type fakeRoles map[uuid.UUID]users.Role

func (f fakeRoles) Get(_ context.Context, id uuid.UUID) (users.User, error) {
	role, ok := f[id]
	if !ok {
		return users.User{}, users.ErrNotFound
	}
	return users.User{ID: id, Role: role}, nil
}

type guardFixture struct {
	clock     *clock.Manual
	tokens    *Tokens
	blocklist *memBlocklist
	svc       *Service
	student   uuid.UUID
	admin     uuid.UUID
	handler   http.Handler
}

func newGuardFixture(t *testing.T) *guardFixture {
	t.Helper()
	f := &guardFixture{
		clock:     clock.NewManual(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)),
		blocklist: &memBlocklist{},
		student:   uuid.New(),
		admin:     uuid.New(),
	}
	f.tokens = NewTokens(testJWTConfig(), f.clock)
	f.svc = &Service{tokens: f.tokens, blocklist: f.blocklist, clock: f.clock}
	roles := fakeRoles{f.student: users.RoleStudent, f.admin: users.RoleAdmin}

	whoami := func(w http.ResponseWriter, r *http.Request) {
		uid, _ := reqctx.UserID(r.Context())
		_, _ = w.Write([]byte(uid.String()))
	}
	mux := http.NewServeMux()
	mux.Handle("/public", http.HandlerFunc(whoami))
	mux.Handle("/private", RequireUser(http.HandlerFunc(whoami)))
	mux.Handle("/admin", RequireRole(roles, users.RoleAdmin)(http.HandlerFunc(whoami)))
	f.handler = httpx.RequestID(Identify(f.svc, FromHeader)(mux))
	return f
}

func (f *guardFixture) access(t *testing.T, id uuid.UUID, role users.Role) string {
	t.Helper()
	tok, _, err := f.tokens.NewAccess(id, string(role))
	require.NoError(t, err)
	return tok
}

func (f *guardFixture) do(path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func TestGuards(t *testing.T) {
	f := newGuardFixture(t)
	studentTok := f.access(t, f.student, users.RoleStudent)

	t.Run("anonymous allowed on public route", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, f.do("/public", "").Code)
	})
	t.Run("anonymous rejected on private route", func(t *testing.T) {
		w := f.do("/private", "")
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), `"code":"unauthorized"`)
	})
	t.Run("valid token reaches handler with user id", func(t *testing.T) {
		w := f.do("/private", studentTok)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, f.student.String(), w.Body.String())
	})
	t.Run("garbage token rejected even on public route", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, f.do("/public", "not.a.jwt").Code)
	})
	t.Run("wrong signing secret", func(t *testing.T) {
		other := NewTokens(config.JWTConfig{
			AccessSecret: []byte(strings.Repeat("x", 32)), RefreshSecret: []byte(strings.Repeat("y", 32)),
			AccessTTL: time.Minute, RefreshTTL: time.Hour, Issuer: "hostelmarket-test",
		}, f.clock)
		tok, _, err := other.NewAccess(f.student, "student")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, f.do("/private", tok).Code)
	})
	t.Run("alg none rejected", func(t *testing.T) {
		claims := AccessClaims{RegisteredClaims: f.tokens.registered(f.student, f.clock.Now(), time.Minute), Type: typeAccess}
		tok, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, f.do("/private", tok).Code)
	})
	t.Run("refresh token cannot be used as access token", func(t *testing.T) {
		tok, _, err := f.tokens.NewRefresh(f.student, uuid.New())
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, f.do("/private", tok).Code)
	})
	t.Run("expired token rejected", func(t *testing.T) {
		tok := f.access(t, f.student, users.RoleStudent)
		f.clock.Advance(16 * time.Minute)
		defer f.clock.Advance(-16 * time.Minute)
		assert.Equal(t, http.StatusUnauthorized, f.do("/private", tok).Code)
	})
	t.Run("logged-out token rejected", func(t *testing.T) {
		tok := f.access(t, f.student, users.RoleStudent)
		p, err := f.svc.Authenticate(context.Background(), tok)
		require.NoError(t, err)
		require.NoError(t, f.blocklist.Block(context.Background(), p.TokenID, p.ExpiresAt))
		w := f.do("/private", tok)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "revoked")
	})
	t.Run("blocklist outage fails closed", func(t *testing.T) {
		f.blocklist.err = errors.New("redis down")
		defer func() { f.blocklist.err = nil }()
		assert.Equal(t, http.StatusServiceUnavailable, f.do("/private", studentTok).Code)
	})
	t.Run("student forbidden on admin route", func(t *testing.T) {
		w := f.do("/admin", studentTok)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})
	t.Run("stale admin claim does not grant admin", func(t *testing.T) {
		// Token says admin, database says student: the database wins.
		assert.Equal(t, http.StatusForbidden, f.do("/admin", f.access(t, f.student, users.RoleAdmin)).Code)
	})
	t.Run("admin allowed on admin route", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, f.do("/admin", f.access(t, f.admin, users.RoleAdmin)).Code)
	})
	t.Run("deleted user rejected on admin route", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, f.do("/admin", f.access(t, uuid.New(), users.RoleAdmin)).Code)
	})
}

func TestTokenSources(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/ws?access_token=qtok", nil)
	assert.Equal(t, "", FromHeader(r), "query token is ignored by the header-only source")
	assert.Equal(t, "qtok", FromHeaderOrQuery(r))

	r.Header.Set("Authorization", "bearer htok")
	assert.Equal(t, "htok", FromHeader(r))
	assert.Equal(t, "htok", FromHeaderOrQuery(r), "header wins over query")

	r.Header.Set("Authorization", "Basic abc")
	assert.Equal(t, "", FromHeader(r))
}
