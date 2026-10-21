package auth

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"hostelmarket/internal/platform/clock"
)

// Blocklist holds the IDs of access tokens revoked before their natural expiry (logout).
type Blocklist interface {
	Block(ctx context.Context, jti string, until time.Time) error
	IsBlocked(ctx context.Context, jti string) (bool, error)
}

// RedisBlocklist keeps auth:blocked:{jti} only until the token would have expired anyway.
type RedisBlocklist struct {
	rdb   redis.Cmdable
	clock clock.Clock
}

func NewRedisBlocklist(rdb redis.Cmdable, clk clock.Clock) *RedisBlocklist {
	return &RedisBlocklist{rdb: rdb, clock: clk}
}

func blockKey(jti string) string { return "auth:blocked:" + jti }

func (b *RedisBlocklist) Block(ctx context.Context, jti string, until time.Time) error {
	ttl := until.Sub(b.clock.Now()) + leeway
	if ttl <= 0 {
		return nil // already expired; nothing to block
	}
	return b.rdb.Set(ctx, blockKey(jti), 1, ttl).Err()
}

func (b *RedisBlocklist) IsBlocked(ctx context.Context, jti string) (bool, error) {
	n, err := b.rdb.Exists(ctx, blockKey(jti)).Result()
	return n > 0, err
}
