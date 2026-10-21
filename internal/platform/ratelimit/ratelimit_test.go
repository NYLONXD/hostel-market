package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hostelmarket/internal/config"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/testutil"
)

// countingStore allows the first n checks per key and rejects the rest.
type countingStore struct {
	mu   sync.Mutex
	seen map[string]int
	err  error
}

func (s *countingStore) Allow(_ context.Context, key string, lim config.Limit) (Result, error) {
	if s.err != nil {
		return Result{}, s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[string]int{}
	}
	s.seen[key]++
	n := s.seen[key]
	if n > lim.Requests {
		return Result{Allowed: false, Limit: lim.Requests, RetryAfter: 1500 * time.Millisecond, ResetAfter: lim.Window}, nil
	}
	return Result{Allowed: true, Limit: lim.Requests, Remaining: lim.Requests - n, ResetAfter: lim.Window}, nil
}

func serve(h http.Handler, ip string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	httpx.RequestID(httpx.ClientIP(false)(h)).ServeHTTP(w, r)
	return w
}

func TestMiddlewareReturns429WithRetryAfter(t *testing.T) {
	p := NewPolicy(&countingStore{}, "test", config.Limit{Requests: 2, Window: time.Minute})
	h := p.Middleware(ByIP)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	assert.Equal(t, http.StatusNoContent, serve(h, "10.0.0.1").Code)
	w := serve(h, "10.0.0.1")
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "0", w.Header().Get("RateLimit-Remaining"))

	w = serve(h, "10.0.0.1")
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "2", w.Header().Get("Retry-After"), "1.5s rounds up")
	assert.Contains(t, w.Body.String(), `"code":"rate_limited"`)

	// Another IP has its own bucket.
	assert.Equal(t, http.StatusNoContent, serve(h, "10.0.0.2").Code)
}

func TestMiddlewareFailsOpen(t *testing.T) {
	p := NewPolicy(&countingStore{err: errors.New("redis down")}, "test", config.Limit{Requests: 1, Window: time.Minute})
	h := p.Middleware(ByIP)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for range 5 {
		assert.Equal(t, http.StatusNoContent, serve(h, "10.0.0.1").Code)
	}
}

func TestByUserSkipsAnonymous(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	assert.Equal(t, "", ByUser(r))
}

// --- Real Redis (runs only with TEST_REDIS_URL) -------------------------------

func TestRedisLimiterAllowsExactlyLimit(t *testing.T) {
	rdb := testutil.Redis(t)
	l := New(rdb)
	key := "it:" + uuid.NewString()
	lim := config.Limit{Requests: 5, Window: 10 * time.Second}

	for i := range 5 {
		res, err := l.Allow(context.Background(), key, lim)
		require.NoError(t, err)
		assert.True(t, res.Allowed, "request %d", i+1)
		assert.Equal(t, 4-i, res.Remaining)
	}
	res, err := l.Allow(context.Background(), key, lim)
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.InDelta(t, 2*time.Second, res.RetryAfter, float64(200*time.Millisecond), "one slot frees every window/limit")
}

func TestRedisLimiterIsAtomicUnderConcurrency(t *testing.T) {
	rdb := testutil.Redis(t)
	l := New(rdb)
	key := "it:" + uuid.NewString()
	lim := config.Limit{Requests: 10, Window: time.Minute}

	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := l.Allow(context.Background(), key, lim)
			if err == nil && res.Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 10, allowed.Load(), "exactly the limit gets through, never more")
}

func TestRedisLimiterRefills(t *testing.T) {
	rdb := testutil.Redis(t)
	l := New(rdb)
	key := "it:" + uuid.NewString()
	lim := config.Limit{Requests: 2, Window: 400 * time.Millisecond}

	for range 2 {
		res, err := l.Allow(context.Background(), key, lim)
		require.NoError(t, err)
		require.True(t, res.Allowed)
	}
	res, _ := l.Allow(context.Background(), key, lim)
	require.False(t, res.Allowed)

	time.Sleep(250 * time.Millisecond) // > one emission interval (200ms)
	res, err := l.Allow(context.Background(), key, lim)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
}
