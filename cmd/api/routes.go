package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	goredis "github.com/redis/go-redis/v9"

	"hostelmarket/internal/auth"
	"hostelmarket/internal/config"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/ratelimit"
	"hostelmarket/internal/users"
)

// restTimeout bounds every non-WebSocket request.
const restTimeout = 30 * time.Second

// deps is the shared infrastructure every module is wired from.
type deps struct {
	cfg   config.Config
	pools *db.Pools
	rdb   *goredis.Client
	db    *db.Router
	clock clock.Clock
}

// modules holds the wired domain modules.
type modules struct {
	limits limits
	users  *users.Service
	auth   *auth.Service
}

type limits struct {
	global, login, register, bid, message *ratelimit.Policy
}

func buildModules(d deps) (*modules, error) {
	store := ratelimit.New(d.rdb)
	rl := d.cfg.RateLimit
	m := &modules{
		limits: limits{
			global:   ratelimit.NewPolicy(store, "global", rl.Global),
			login:    ratelimit.NewPolicy(store, "login", rl.Login),
			register: ratelimit.NewPolicy(store, "register", rl.Register),
			bid:      ratelimit.NewPolicy(store, "bid", rl.Bid),
			message:  ratelimit.NewPolicy(store, "message", rl.Message),
		},
		users: users.NewService(d.db),
	}

	var err error
	m.auth, err = auth.NewService(auth.Options{
		Users:               m.users,
		DB:                  d.db,
		Tokens:              auth.NewTokens(d.cfg.JWT, d.clock),
		Blocklist:           auth.NewRedisBlocklist(d.rdb, d.clock),
		Clock:               d.clock,
		CollegeEmailDomains: d.cfg.Auth.CollegeEmailDomains,
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

func newHTTPHandler(d deps, m *modules) http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.RequestID, httpx.ClientIP(d.cfg.TrustProxyHeaders), httpx.AccessLog, httpx.Recoverer)

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
		// Identify first so the global limit can bucket per user rather than per IP.
		r.Use(auth.Identify(m.auth, auth.FromHeader))
		r.Use(m.limits.global.Middleware(ratelimit.ByUserOrIP))

		// REST endpoints. (WebSocket routes will mount outside this group: a
		// request timeout would cut long-lived connections.)
		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(restTimeout))

			auth.NewHandler(m.auth, m.limits.login, m.limits.register).Routes(r)

			r.Group(func(r chi.Router) {
				r.Use(auth.RequireUser)
				users.NewHandler(m.users).Routes(r)
			})
		})
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
