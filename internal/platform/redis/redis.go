// Package redis opens the shared go-redis client.
package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Open parses a redis:// or rediss:// (TLS, e.g. Redis Cloud) URL, connects and pings.
// poolSize <= 0 keeps go-redis's default.
func Open(ctx context.Context, url string, poolSize int) (*goredis.Client, error) {
	opts, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	if poolSize > 0 {
		opts.PoolSize = poolSize
	}
	client := goredis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis at %s: %w", opts.Addr, err)
	}
	return client, nil
}
