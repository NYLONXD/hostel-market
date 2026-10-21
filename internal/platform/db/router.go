package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"hostelmarket/internal/platform/reqctx"
)

// Consistency says how fresh a read must be.
type Consistency int

const (
	// ReplicaOK reads may come from the replica unless the acting user wrote in the
	// last few seconds. Use only for browsing data (listing feed, listing detail, profiles).
	ReplicaOK Consistency = iota
	// Strong reads always hit the primary. Required for orders, payments, bids,
	// availability/eligibility decisions and auth data.
	Strong
)

// RecentWrites remembers which users just wrote, so their reads skip the lagging replica.
type RecentWrites interface {
	Mark(ctx context.Context, userID uuid.UUID) error
	Has(ctx context.Context, userID uuid.UUID) (bool, error)
}

// Router is the one place that chooses primary vs replica.
type Router struct {
	pools  *Pools
	recent RecentWrites
	log    *slog.Logger
}

func NewRouter(pools *Pools, recent RecentWrites, log *slog.Logger) *Router {
	return &Router{pools: pools, recent: recent, log: log}
}

// Primary is for writes and Strong reads that need pool-level APIs.
func (r *Router) Primary() *pgxpool.Pool { return r.pools.Primary }

// Reader returns the pool a read should use.
func (r *Router) Reader(ctx context.Context, c Consistency) DBTX {
	if c == Strong || !r.pools.HasReplica() {
		return r.pools.Primary
	}
	if r.RecentlyWrote(ctx) {
		return r.pools.Primary
	}
	return r.pools.Replica
}

// RecentlyWrote reports whether the acting user wrote within the recent-write TTL.
// Callers also use it to bypass caches. Errors fail safe (true => primary, no cache).
func (r *Router) RecentlyWrote(ctx context.Context) bool {
	uid, ok := reqctx.UserID(ctx)
	if !ok {
		return false
	}
	recent, err := r.recent.Has(ctx, uid)
	if err != nil {
		r.log.WarnContext(ctx, "recent-write check failed; using primary", "err", err)
		return true
	}
	return recent
}

// WithTx runs fn in a primary transaction. After a successful commit the acting user is
// marked as a recent writer so their follow-up reads see their own write.
func (r *Router) WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := r.pools.Primary.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }() // no-op after commit

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	r.MarkWrite(ctx)
	return nil
}

// MarkWrite records that the acting user just wrote. A failure only costs
// read-your-writes for a few seconds, so it is logged rather than returned.
func (r *Router) MarkWrite(ctx context.Context) {
	uid, ok := reqctx.UserID(ctx)
	if !ok {
		return
	}
	if err := r.recent.Mark(context.WithoutCancel(ctx), uid); err != nil {
		r.log.WarnContext(ctx, "mark recent write failed", "err", err)
	}
}

// RedisRecentWrites stores recent_write:{userID} keys with a short TTL.
type RedisRecentWrites struct {
	rdb redis.Cmdable
	ttl time.Duration
}

func NewRedisRecentWrites(rdb redis.Cmdable, ttl time.Duration) *RedisRecentWrites {
	return &RedisRecentWrites{rdb: rdb, ttl: ttl}
}

func recentWriteKey(userID uuid.UUID) string { return "recent_write:" + userID.String() }

func (w *RedisRecentWrites) Mark(ctx context.Context, userID uuid.UUID) error {
	return w.rdb.Set(ctx, recentWriteKey(userID), 1, w.ttl).Err()
}

func (w *RedisRecentWrites) Has(ctx context.Context, userID uuid.UUID) (bool, error) {
	n, err := w.rdb.Exists(ctx, recentWriteKey(userID)).Result()
	return n > 0, err
}
