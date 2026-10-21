// Package db owns the Postgres connection pools and the single helper that decides
// whether a read may be served by the replica.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx, and is what sqlc-generated code expects.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type PoolConfig struct {
	PrimaryURL string
	ReplicaURL string // empty => the replica pool is the primary pool
	MaxConns   int32
}

// Pools holds the primary (all writes, strong reads) and replica (lag-tolerant reads).
// When no replica is configured both fields point at the same pool, so local dev
// works against a single Postgres.
type Pools struct {
	Primary *pgxpool.Pool
	Replica *pgxpool.Pool
}

// Open connects and pings both pools.
func Open(ctx context.Context, cfg PoolConfig) (*Pools, error) {
	primary, err := newPool(ctx, cfg.PrimaryURL, cfg.MaxConns)
	if err != nil {
		return nil, fmt.Errorf("primary: %w", err)
	}
	if cfg.ReplicaURL == "" || cfg.ReplicaURL == cfg.PrimaryURL {
		return &Pools{Primary: primary, Replica: primary}, nil
	}
	replica, err := newPool(ctx, cfg.ReplicaURL, cfg.MaxConns)
	if err != nil {
		primary.Close()
		return nil, fmt.Errorf("replica: %w", err)
	}
	return &Pools{Primary: primary, Replica: replica}, nil
}

func newPool(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if maxConns > 0 {
		pc.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping %s:%d/%s: %w", pc.ConnConfig.Host, pc.ConnConfig.Port, pc.ConnConfig.Database, err)
	}
	return pool, nil
}

// HasReplica reports whether reads can actually be offloaded.
func (p *Pools) HasReplica() bool { return p.Replica != p.Primary }

// Ping checks both pools (used by /readyz).
func (p *Pools) Ping(ctx context.Context) (primaryErr, replicaErr error) {
	primaryErr = p.Primary.Ping(ctx)
	if p.HasReplica() {
		replicaErr = p.Replica.Ping(ctx)
	} else {
		replicaErr = primaryErr
	}
	return primaryErr, replicaErr
}

func (p *Pools) Close() {
	if p.HasReplica() {
		p.Replica.Close()
	}
	p.Primary.Close()
}

// IsUniqueViolation reports whether err is a Postgres unique-constraint failure,
// optionally on a specific constraint or index name.
func IsUniqueViolation(err error, constraint ...string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	if len(constraint) == 0 {
		return true
	}
	for _, c := range constraint {
		if pgErr.ConstraintName == c {
			return true
		}
	}
	return false
}

// IsNoRows reports whether err means the query matched nothing.
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
