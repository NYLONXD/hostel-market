// Package cache is a small versioned JSON cache on Redis. Invalidation bumps a
// namespace version instead of hunting down keys, so a whole feed (every filter,
// every page) is invalidated with one INCR and old entries simply age out.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

type Versioned struct {
	rdb redis.Cmdable
	ttl time.Duration
	// reinvalidate bumps the version a second time after this delay. A reader that
	// raced the first bump may have refilled the cache from a lagging replica; the
	// second bump discards that. 0 disables it.
	reinvalidate time.Duration
}

func NewVersioned(rdb redis.Cmdable, ttl, reinvalidate time.Duration) *Versioned {
	return &Versioned{rdb: rdb, ttl: ttl, reinvalidate: reinvalidate}
}

func versionKey(ns string) string { return "cache:ver:" + ns }

func (c *Versioned) entryKey(ctx context.Context, ns, key string) (string, error) {
	ver, err := c.rdb.Get(ctx, versionKey(ns)).Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return "", err
	}
	return fmt.Sprintf("cache:%s:v%d:%s", ns, ver, key), nil
}

// Get decodes a cached value into dst. Misses and Redis errors both report false;
// a cache must never fail the request.
func (c *Versioned) Get(ctx context.Context, ns, key string, dst any) bool {
	k, err := c.entryKey(ctx, ns, key)
	if err != nil {
		slog.WarnContext(ctx, "cache version read failed", "ns", ns, "err", err)
		return false
	}
	b, err := c.rdb.Get(ctx, k).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			slog.WarnContext(ctx, "cache read failed", "ns", ns, "err", err)
		}
		return false
	}
	return json.Unmarshal(b, dst) == nil
}

// Set stores v under the namespace's current version.
func (c *Versioned) Set(ctx context.Context, ns, key string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.WarnContext(ctx, "cache encode failed", "ns", ns, "err", err)
		return
	}
	k, err := c.entryKey(ctx, ns, key)
	if err == nil {
		err = c.rdb.Set(ctx, k, b, c.ttl).Err()
	}
	if err != nil {
		slog.WarnContext(ctx, "cache write failed", "ns", ns, "err", err)
	}
}

// Invalidate makes every entry in the namespace unreachable.
func (c *Versioned) Invalidate(ctx context.Context, ns string) {
	c.bump(context.WithoutCancel(ctx), ns)
	if c.reinvalidate > 0 {
		bg := context.WithoutCancel(ctx)
		time.AfterFunc(c.reinvalidate, func() { c.bump(bg, ns) })
	}
}

func (c *Versioned) bump(ctx context.Context, ns string) {
	if err := c.rdb.Incr(ctx, versionKey(ns)).Err(); err != nil {
		// Entries still expire after the TTL, so staleness is bounded.
		slog.WarnContext(ctx, "cache invalidation failed", "ns", ns, "err", err)
	}
}
