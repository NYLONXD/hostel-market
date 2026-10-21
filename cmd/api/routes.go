package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	goredis "github.com/redis/go-redis/v9"

	"hostelmarket/internal/config"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/httpx"
)

// deps is everything the modules are wired from.
type deps struct {
	cfg   config.Config
	pools *db.Pools
	rdb   *goredis.Client
	db    *db.Router
}

func newHTTPHandler(d deps) http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.RequestID, httpx.AccessLog, httpx.Recoverer)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.NotFound("route not found"))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.NewError(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed"))
	})

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", readyz(d))

	r.Route("/v1", func(r chi.Router) {
		// Modules mount here as they are built.
	})
	return r
}

// readyz reports whether every dependency answers; load balancers should gate on it.
func readyz(d deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		primaryErr, replicaErr := d.pools.Ping(ctx)
		results := map[string]error{
			"postgres_primary": primaryErr,
			"postgres_replica": replicaErr,
			"redis":            d.rdb.Ping(ctx).Err(),
		}

		status, overall := http.StatusOK, "ok"
		checks := make(map[string]string, len(results))
		for name, err := range results {
			checks[name] = "ok"
			if err != nil {
				checks[name] = "unavailable"
				status, overall = http.StatusServiceUnavailable, "unavailable"
				slog.WarnContext(r.Context(), "readiness check failed", "check", name, "err", err)
			}
		}
		httpx.WriteJSON(w, status, map[string]any{"status": overall, "checks": checks})
	}
}
