// Package ratelimit implements Redis-backed GCRA rate limiting (one Lua call per check,
// atomic across all API instances) plus HTTP middleware that answers 429 + Retry-After.
package ratelimit

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"hostelmarket/internal/config"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/reqctx"
)

//go:embed gcra.lua
var gcraSource string

var gcraScript = redis.NewScript(gcraSource)

type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
	ResetAfter time.Duration
}

// Store decides a single check. *Limiter is the Redis implementation.
type Store interface {
	Allow(ctx context.Context, key string, lim config.Limit) (Result, error)
}

type Limiter struct {
	rdb redis.Scripter
}

func New(rdb redis.Scripter) *Limiter { return &Limiter{rdb: rdb} }

func (l *Limiter) Allow(ctx context.Context, key string, lim config.Limit) (Result, error) {
	if lim.Requests < 1 || lim.Window <= 0 {
		return Result{}, fmt.Errorf("ratelimit: invalid limit %s", lim)
	}
	interval := max(lim.Window.Milliseconds()/int64(lim.Requests), 1)
	capacity := interval * int64(lim.Requests)

	vals, err := gcraScript.Run(ctx, l.rdb, []string{"rl:" + key}, interval, capacity).Int64Slice()
	if err != nil {
		return Result{}, fmt.Errorf("ratelimit: run script: %w", err)
	}
	if len(vals) != 4 {
		return Result{}, fmt.Errorf("ratelimit: unexpected script reply %v", vals)
	}
	return Result{
		Allowed:    vals[0] == 1,
		Limit:      lim.Requests,
		Remaining:  int(vals[1]),
		RetryAfter: time.Duration(vals[2]) * time.Millisecond,
		ResetAfter: time.Duration(vals[3]) * time.Millisecond,
	}, nil
}

// Policy is a named limit, e.g. "login" at 5/15m.
type Policy struct {
	name  string
	limit config.Limit
	store Store
}

func NewPolicy(store Store, name string, lim config.Limit) *Policy {
	return &Policy{name: name, limit: lim, store: store}
}

// Check consumes one request for key. It returns a 429 *httpx.Error when the limit is
// exceeded. Store failures fail open (logged) so a Redis blip doesn't take the API down.
func (p *Policy) Check(ctx context.Context, key string) (Result, error) {
	res, err := p.store.Allow(ctx, p.name+":"+key, p.limit)
	if err != nil {
		slog.WarnContext(ctx, "rate limiter unavailable; allowing request", "policy", p.name, "err", err)
		return Result{Allowed: true, Limit: p.limit.Requests, Remaining: -1}, nil
	}
	if !res.Allowed {
		return res, &httpx.Error{
			Status:  http.StatusTooManyRequests,
			Code:    "rate_limited",
			Message: "too many requests, please slow down",
			Details: map[string]any{"policy": p.name, "retry_after_seconds": ceilSeconds(res.RetryAfter)},
			Headers: map[string]string{"Retry-After": strconv.Itoa(ceilSeconds(res.RetryAfter))},
		}
	}
	return res, nil
}

// Enforce is Check without the result, for callers that only need allow/deny.
func (p *Policy) Enforce(ctx context.Context, key string) error {
	_, err := p.Check(ctx, key)
	return err
}

// KeyFunc picks the bucket for a request; "" skips limiting.
type KeyFunc func(r *http.Request) string

// Middleware applies the policy to every request and sets RateLimit-* headers.
func (p *Policy) Middleware(key KeyFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k := key(r)
			if k == "" {
				next.ServeHTTP(w, r)
				return
			}
			res, err := p.Check(r.Context(), k)
			if res.Remaining >= 0 {
				h := w.Header()
				h.Set("RateLimit-Limit", strconv.Itoa(res.Limit))
				h.Set("RateLimit-Remaining", strconv.Itoa(res.Remaining))
				h.Set("RateLimit-Reset", strconv.Itoa(ceilSeconds(res.ResetAfter)))
			}
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ByUserOrIP buckets authenticated callers per user and anonymous ones per IP.
func ByUserOrIP(r *http.Request) string {
	if uid, ok := reqctx.UserID(r.Context()); ok {
		return "u:" + uid.String()
	}
	return ByIP(r)
}

func ByIP(r *http.Request) string {
	if ip := reqctx.ClientIP(r.Context()); ip != "" {
		return "ip:" + ip
	}
	return "ip:" + r.RemoteAddr
}

// ByUser buckets per authenticated user and skips anonymous requests (auth rejects them anyway).
func ByUser(r *http.Request) string {
	if uid, ok := reqctx.UserID(r.Context()); ok {
		return "u:" + uid.String()
	}
	return ""
}

func ceilSeconds(d time.Duration) int {
	return int(math.Ceil(d.Seconds()))
}
