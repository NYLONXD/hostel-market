// Package testutil provides real Postgres/Redis for integration tests. Tests that need
// them are skipped unless TEST_DATABASE_URL / TEST_REDIS_URL are set.
package testutil

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"hostelmarket/internal/platform/db"
)

// Postgres returns a pool whose search_path is a fresh schema with every migration
// applied. Each call gets its own schema, so test packages can run in parallel
// against one database. The schema is dropped when the test ends.
func Postgres(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect TEST_DATABASE_URL: %v", err)
	}
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 50 // concurrency tests open many transactions at once
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = admin.Exec(c, "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(c)
	})

	for _, file := range upMigrations(t) {
		sql, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		// No arguments => simple protocol, so multi-statement files run as-is.
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(file), err)
		}
	}
	return pool
}

// Router wraps a test pool (as both primary and replica) in a db.Router.
func Router(pool *pgxpool.Pool, recent db.RecentWrites) *db.Router {
	if recent == nil {
		recent = NoRecentWrites{}
	}
	return db.NewRouter(&db.Pools{Primary: pool, Replica: pool}, recent, Logger())
}

// Redis returns a client for TEST_REDIS_URL, skipping the test when unset.
func Redis(t testing.TB) *redis.Client {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set; skipping integration test")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse TEST_REDIS_URL: %v", err)
	}
	opts.PoolSize = 200
	client := redis.NewClient(opts)
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("ping redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// Logger discards output; tests assert on behaviour, not logs.
func Logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// NoRecentWrites is a db.RecentWrites that never reports a recent write.
type NoRecentWrites struct{}

func (NoRecentWrites) Mark(context.Context, uuid.UUID) error        { return nil }
func (NoRecentWrites) Has(context.Context, uuid.UUID) (bool, error) { return false, nil }

func upMigrations(t testing.TB) []string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "migrations")
	files, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found in %s", dir)
	}
	sort.Strings(files)
	return files
}

func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above working directory")
		}
		dir = parent
	}
}
