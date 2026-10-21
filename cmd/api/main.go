// Command api runs the hostel marketplace HTTP API (and, unless RUN_WORKERS=false,
// the background job worker) as a single modular-monolith process.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hostelmarket/internal/config"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/logx"
	platformredis "hostelmarket/internal/platform/redis"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := logx.New(os.Stdout, cfg.Env, cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pools, err := db.Open(ctx, db.PoolConfig{
		PrimaryURL: cfg.DB.PrimaryURL,
		ReplicaURL: cfg.DB.ReplicaURL,
		MaxConns:   cfg.DB.MaxConns,
	})
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pools.Close()
	if !pools.HasReplica() {
		logger.Info("no read replica configured; all reads use the primary")
	}

	rdb, err := platformredis.Open(ctx, cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer rdb.Close()

	d := deps{
		cfg:   cfg,
		pools: pools,
		rdb:   rdb,
		db:    db.NewRouter(pools, db.NewRedisRecentWrites(rdb, cfg.Cache.RecentWriteTTL), logger),
	}

	// No server-wide Read/WriteTimeout: they would also apply to hijacked WebSocket
	// connections. REST routes get a per-request timeout in the router instead.
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           newHTTPHandler(d),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down", "timeout", cfg.ShutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}
