package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	goredis "github.com/redis/go-redis/v9"

	"hostelmarket/internal/admin"
	"hostelmarket/internal/auth"
	"hostelmarket/internal/bidding"
	"hostelmarket/internal/chat"
	"hostelmarket/internal/config"
	"hostelmarket/internal/listings"
	"hostelmarket/internal/media"
	"hostelmarket/internal/orders"
	"hostelmarket/internal/payments"
	"hostelmarket/internal/platform/cache"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/jobs"
	"hostelmarket/internal/platform/ratelimit"
	"hostelmarket/internal/platform/ws"
	"hostelmarket/internal/users"
)

const (
	// restTimeout bounds every non-WebSocket request.
	restTimeout = 30 * time.Second
	// feedReinvalidate is the second feed-cache bump after a write, covering a reader
	// that refilled the cache from a replica that hadn't caught up yet.
	feedReinvalidate = 2 * time.Second
)

// deps is the shared infrastructure every module is wired from.
type deps struct {
	cfg   config.Config
	pools *db.Pools
	rdb   *goredis.Client
	db    *db.Router
	jobs  *jobs.Client
	hub   *ws.Hub
	clock clock.Clock
	log   *slog.Logger
}

// modules holds the wired domain modules.
type modules struct {
	limits   limits
	users    *users.Service
	auth     *auth.Service
	media    *media.Service
	listings *listings.Service
	orders   *orders.Service
	bidding  *bidding.Service
	payments *payments.Service
	chat     *chat.Service
	gateway  *ws.Gateway
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

	cld, err := media.NewCloudinary(d.cfg.Cloudinary)
	if err != nil {
		return nil, err
	}
	if cld == nil {
		d.log.Warn("Cloudinary not configured; photo uploads are disabled")
	}
	m.media = media.NewService(d.cfg.Cloudinary, cld, d.jobs, d.clock)

	feed := cache.NewVersioned(d.rdb, d.cfg.Cache.FeedTTL, feedReinvalidate)
	m.listings = listings.NewService(d.db, m.users, m.media, feed, d.clock)

	mk := d.cfg.Market
	m.orders = orders.NewService(d.db, m.listings, m.users, d.jobs, d.hub, d.clock, mk.OrderRequestTTL)
	m.listings.OnRemove(m.orders)

	m.bidding = bidding.NewService(d.db, d.rdb, m.listings, m.users, m.orders, d.hub, d.jobs, d.clock, bidding.Config{
		AntiSnipeWindow:    mk.AntiSnipeWindow,
		AntiSnipeExtension: mk.AntiSnipeExtension,
		MaxPaymentDefaults: int32(mk.MaxPaymentDefaults),
	})
	// orders and bidding need each other; they meet only through interfaces.
	m.orders.EnableAuctions(m.bidding, orders.AuctionConfig{
		FeeBps:             mk.PlatformFeeBps,
		PaymentDeadline:    mk.AuctionPaymentDeadline,
		MaxPaymentDefaults: int32(mk.MaxPaymentDefaults),
	})
	m.listings.SetAuctionHooks(m.bidding)

	var provider payments.Provider
	switch d.cfg.Payments.Provider {
	case config.ProviderUPILink:
		provider = payments.UPILinkProvider{PayeeVPA: d.cfg.Payments.PlatformUPIID, PayeeName: d.cfg.Payments.PlatformUPIName}
	default:
		provider = payments.NewMockProvider(d.cfg.Payments.MockWebhookSecret, d.clock)
	}
	m.payments = payments.NewService(payments.Options{
		DB: d.db, Orders: m.orders, Provider: provider, Jobs: d.jobs, Notify: d.hub, Clock: d.clock,
		PendingGrace: mk.PaymentPendingGrace, WebhookBaseURL: d.cfg.PublicBaseURL,
	})
	m.orders.SetPaymentGuard(m.payments)

	m.chat = chat.NewService(d.db, m.listings, m.users, d.hub, m.limits.message)
	m.gateway = ws.NewGateway(d.hub, d.cfg.WSOriginPatterns, d.log)
	chat.NewHandler(m.chat).RegisterWS(m.gateway)
	bidding.NewHandler(m.bidding, m.limits.bid).RegisterWS(m.gateway)
	return m, nil
}

// registerJobs attaches every module's background handlers to the worker.
func registerJobs(w *jobs.Worker, m *modules) error {
	w.Handle(media.TaskDeleteAssets, m.media.HandleDeleteTask)
	m.payments.RegisterJobs(w)
	if err := m.orders.RegisterJobs(w); err != nil {
		return err
	}
	return m.bidding.RegisterJobs(w)
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

		// WebSocket: outside the REST timeout group. Browsers can't set headers on the
		// upgrade, so the token may also come as ?access_token=.
		r.With(auth.Identify(m.auth, auth.FromHeaderOrQuery), auth.RequireUser).
			Get("/ws", func(w http.ResponseWriter, r *http.Request) {
				p, _ := auth.PrincipalFrom(r.Context())
				m.gateway.Serve(w, r, p.UserID, p.ExpiresAt)
			})

		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(restTimeout))

			auth.NewHandler(m.auth, m.limits.login, m.limits.register).Routes(r)
			payHandler := payments.NewHandler(m.payments)
			payHandler.WebhookRoutes(r) // authenticated by provider signature, not JWT

			r.Group(func(r chi.Router) {
				r.Use(auth.RequireUser)
				users.NewHandler(m.users).Routes(r)
				media.NewHandler(m.media, m.users).Routes(r)
				listings.NewHandler(m.listings, m.users, m.bidding, d.clock).Routes(r)
				bidding.NewHandler(m.bidding, m.limits.bid).Routes(r)
				orders.NewHandler(m.orders).Routes(r)
				payHandler.Routes(r)
				chat.NewHandler(m.chat).Routes(r)
				if d.cfg.IsDevelopment() && d.cfg.Payments.Provider == config.ProviderMock {
					payHandler.DevRoutes(r)
				}
			})

			r.Group(func(r chi.Router) {
				r.Use(auth.RequireUser, auth.RequireRole(m.users, users.RoleAdmin))
				admin.NewHandler(m.users, m.listings, m.payments).Routes(r)
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
