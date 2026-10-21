package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/testutil"
	"hostelmarket/internal/users"
)

func newIntegrationService(t *testing.T) (*Service, *memBlocklist) {
	t.Helper()
	router := testutil.Router(testutil.Postgres(t), nil)
	bl := &memBlocklist{}
	clk := clock.System{}
	svc, err := NewService(Options{
		Users:               users.NewService(router),
		DB:                  router,
		Tokens:              NewTokens(testJWTConfig(), clk),
		Blocklist:           bl,
		Clock:               clk,
		CollegeEmailDomains: []string{"college.ac.in"},
		BcryptCost:          bcrypt.MinCost,
	})
	require.NoError(t, err)
	return svc, bl
}

func requireStatus(t *testing.T, err error, status int) {
	t.Helper()
	var apiErr *httpx.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, status, apiErr.Status, apiErr.Message)
}

func TestRegisterLoginRefreshLogout(t *testing.T) {
	svc, _ := newIntegrationService(t)
	ctx := context.Background()

	sess, err := svc.Register(ctx, RegisterInput{
		Email: "  Asha@College.ac.in ", Password: "correct horse", Name: "Asha",
		Hostel: "brahmaputra", Block: " b ", Year: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, "asha@college.ac.in", sess.User.Email)
	assert.Equal(t, "BRAHMAPUTRA", sess.User.Hostel)
	assert.Equal(t, "B", sess.User.Block)
	assert.True(t, sess.User.IsVerified, "college domain auto-verifies")

	_, err = svc.Register(ctx, RegisterInput{Email: "asha@college.ac.in", Password: "whatever1", Name: "X", Hostel: "H", Block: "B", Year: 1})
	requireStatus(t, err, http.StatusConflict)

	_, err = svc.Login(ctx, "asha@college.ac.in", "wrong password")
	requireStatus(t, err, http.StatusUnauthorized)
	_, err = svc.Login(ctx, "nobody@college.ac.in", "correct horse")
	requireStatus(t, err, http.StatusUnauthorized)

	login, err := svc.Login(ctx, "ASHA@college.ac.in", "correct horse")
	require.NoError(t, err)

	// Rotation: the new pair works, the old refresh token is spent.
	rotated, err := svc.Refresh(ctx, login.RefreshToken)
	require.NoError(t, err)
	assert.NotEqual(t, login.RefreshToken, rotated.RefreshToken)

	// Reusing the spent token revokes the family, killing the rotated token too.
	_, err = svc.Refresh(ctx, login.RefreshToken)
	requireStatus(t, err, http.StatusUnauthorized)
	_, err = svc.Refresh(ctx, rotated.RefreshToken)
	requireStatus(t, err, http.StatusUnauthorized)

	// A separate login is a separate family and is unaffected.
	other, err := svc.Login(ctx, "asha@college.ac.in", "correct horse")
	require.NoError(t, err)
	p, err := svc.Authenticate(ctx, other.AccessToken)
	require.NoError(t, err)

	require.NoError(t, svc.Logout(ctx, p, other.RefreshToken))
	_, err = svc.Authenticate(ctx, other.AccessToken)
	requireStatus(t, err, http.StatusUnauthorized)
	_, err = svc.Refresh(ctx, other.RefreshToken)
	requireStatus(t, err, http.StatusUnauthorized)
}

func TestNonCollegeEmailIsUnverified(t *testing.T) {
	svc, _ := newIntegrationService(t)
	sess, err := svc.Register(context.Background(), RegisterInput{
		Email: "ravi@gmail.com", Password: "password123", Name: "Ravi", Hostel: "H1", Block: "A", Year: 1,
	})
	require.NoError(t, err)
	assert.False(t, sess.User.IsVerified)
	assert.False(t, sess.User.CanSell())
}

func TestExpiredRefreshRejected(t *testing.T) {
	svc, _ := newIntegrationService(t)
	ctx := context.Background()
	sess, err := svc.Register(ctx, RegisterInput{
		Email: "old@college.ac.in", Password: "password123", Name: "Old", Hostel: "H1", Block: "A", Year: 3,
	})
	require.NoError(t, err)

	later := clock.NewManual(time.Now().Add(25 * time.Hour)) // past the 24h test refresh TTL
	svc.tokens = NewTokens(testJWTConfig(), later)
	svc.clock = later
	_, err = svc.Refresh(ctx, sess.RefreshToken)
	requireStatus(t, err, http.StatusUnauthorized)
}
