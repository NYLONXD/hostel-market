// Package config loads and validates all runtime settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"hostelmarket/internal/platform/money"
)

const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
	EnvTest        = "test"

	ProviderMock    = "mock"
	ProviderUPILink = "upi_link"
)

type Config struct {
	Env             string
	HTTPAddr        string
	PublicBaseURL   string // used by the mock provider to call our own webhook
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	RunWorkers      bool // run the asynq worker + scheduler in this process
	// TrustProxyHeaders makes X-Forwarded-For / X-Real-IP the client IP. Enable only
	// behind a proxy that overwrites them, or clients can spoof their IP.
	TrustProxyHeaders bool

	DB         DBConfig
	RedisURL   string
	JWT        JWTConfig
	Auth       AuthConfig
	Market     MarketConfig
	Cache      CacheConfig
	RateLimit  RateLimitConfig
	Cloudinary CloudinaryConfig
	Payments   PaymentsConfig
}

type DBConfig struct {
	PrimaryURL string
	ReplicaURL string // empty => reads fall back to the primary
	MaxConns   int32
}

type JWTConfig struct {
	AccessSecret  []byte
	RefreshSecret []byte
	AccessTTL     time.Duration
	RefreshTTL    time.Duration
	Issuer        string
}

type AuthConfig struct {
	// Users registering with one of these email domains are auto-verified.
	CollegeEmailDomains []string
}

type MarketConfig struct {
	PlatformFeeBps         int64 // parsed from PLATFORM_FEE_PERCENT
	AuctionPaymentDeadline time.Duration
	PaymentPendingGrace    time.Duration
	AntiSnipeWindow        time.Duration
	AntiSnipeExtension     time.Duration
	OrderRequestTTL        time.Duration
	MaxPaymentDefaults     int
}

type CacheConfig struct {
	RecentWriteTTL time.Duration
	FeedTTL        time.Duration
}

// Limit allows Requests per Window.
type Limit struct {
	Requests int
	Window   time.Duration
}

func (l Limit) String() string { return fmt.Sprintf("%d/%s", l.Requests, l.Window) }

type RateLimitConfig struct {
	Global   Limit
	Login    Limit
	Register Limit
	Bid      Limit
	Message  Limit
}

type CloudinaryConfig struct {
	CloudName string
	APIKey    string
	APISecret string
	Folder    string
}

func (c CloudinaryConfig) Enabled() bool {
	return c.CloudName != "" && c.APIKey != "" && c.APISecret != ""
}

type PaymentsConfig struct {
	Provider          string
	PlatformUPIID     string
	PlatformUPIName   string
	MockWebhookSecret string
}

func (c Config) IsDevelopment() bool { return c.Env == EnvDevelopment }

// Load reads the process environment.
func Load() (Config, error) { return load(os.Getenv) }

func load(getenv func(string) string) (Config, error) {
	r := &reader{get: getenv}

	cfg := Config{
		Env:             r.str("APP_ENV", EnvDevelopment),
		HTTPAddr:        r.str("HTTP_ADDR", ":8080"),
		PublicBaseURL:   strings.TrimRight(r.str("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		LogLevel:        r.logLevel("LOG_LEVEL", slog.LevelInfo),
		ShutdownTimeout: r.duration("SHUTDOWN_TIMEOUT", 15*time.Second),
		RunWorkers:      r.bool("RUN_WORKERS", true),

		TrustProxyHeaders: r.bool("TRUST_PROXY_HEADERS", false),
		DB: DBConfig{
			PrimaryURL: r.required("DATABASE_URL_PRIMARY"),
			ReplicaURL: r.str("DATABASE_URL_REPLICA", ""),
			MaxConns:   int32(r.int("DB_MAX_CONNS", 20)),
		},
		RedisURL: r.required("REDIS_URL"),
		JWT: JWTConfig{
			AccessSecret:  []byte(r.required("JWT_ACCESS_SECRET")),
			RefreshSecret: []byte(r.required("JWT_REFRESH_SECRET")),
			AccessTTL:     r.duration("JWT_ACCESS_TTL", 15*time.Minute),
			RefreshTTL:    r.duration("JWT_REFRESH_TTL", 30*24*time.Hour),
			Issuer:        r.str("JWT_ISSUER", "hostelmarket"),
		},
		Auth: AuthConfig{
			CollegeEmailDomains: r.list("COLLEGE_EMAIL_DOMAINS"),
		},
		Market: MarketConfig{
			PlatformFeeBps:         r.percent("PLATFORM_FEE_PERCENT", "20"),
			AuctionPaymentDeadline: r.duration("AUCTION_PAYMENT_DEADLINE", 30*time.Minute),
			PaymentPendingGrace:    r.duration("PAYMENT_PENDING_GRACE", 30*time.Minute),
			AntiSnipeWindow:        r.duration("ANTI_SNIPE_WINDOW", 30*time.Second),
			AntiSnipeExtension:     r.duration("ANTI_SNIPE_EXTENSION", 60*time.Second),
			OrderRequestTTL:        r.duration("ORDER_REQUEST_TTL", 48*time.Hour),
			MaxPaymentDefaults:     r.int("MAX_PAYMENT_DEFAULTS", 2),
		},
		Cache: CacheConfig{
			RecentWriteTTL: r.duration("RECENT_WRITE_TTL", 10*time.Second),
			FeedTTL:        r.duration("FEED_CACHE_TTL", 30*time.Second),
		},
		RateLimit: RateLimitConfig{
			Global: r.limit("RATE_LIMIT_GLOBAL", Limit{120, time.Minute}),
			Login:  r.limit("RATE_LIMIT_LOGIN", Limit{5, 15 * time.Minute}),
			// Per IP. A whole hostel often shares one NAT IP, so this can't be tiny.
			Register: r.limit("RATE_LIMIT_REGISTER", Limit{20, time.Hour}),
			Bid:      r.limit("RATE_LIMIT_BID", Limit{20, time.Minute}),
			Message:  r.limit("RATE_LIMIT_MESSAGE", Limit{30, time.Minute}),
		},
		Cloudinary: CloudinaryConfig{
			CloudName: r.str("CLOUDINARY_CLOUD_NAME", ""),
			APIKey:    r.str("CLOUDINARY_API_KEY", ""),
			APISecret: r.str("CLOUDINARY_API_SECRET", ""),
			Folder:    strings.Trim(r.str("CLOUDINARY_FOLDER", "hostel_market"), "/"),
		},
		Payments: PaymentsConfig{
			Provider:          r.str("PAYMENT_PROVIDER", ProviderMock),
			PlatformUPIID:     r.str("PLATFORM_UPI_ID", ""),
			PlatformUPIName:   r.str("PLATFORM_UPI_NAME", "Hostel Market"),
			MockWebhookSecret: r.str("MOCK_WEBHOOK_SECRET", ""),
		},
	}

	cfg.validate(r)
	if err := errors.Join(r.errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration:\n%w", err)
	}
	return cfg, nil
}

func (c *Config) validate(r *reader) {
	switch c.Env {
	case EnvDevelopment, EnvProduction, EnvTest:
	default:
		r.fail("APP_ENV must be one of development, production, test")
	}

	const minSecret = 32
	if n := len(c.JWT.AccessSecret); n > 0 && n < minSecret {
		r.fail("JWT_ACCESS_SECRET must be at least %d bytes", minSecret)
	}
	if n := len(c.JWT.RefreshSecret); n > 0 && n < minSecret {
		r.fail("JWT_REFRESH_SECRET must be at least %d bytes", minSecret)
	}
	if len(c.JWT.AccessSecret) > 0 && string(c.JWT.AccessSecret) == string(c.JWT.RefreshSecret) {
		r.fail("JWT_ACCESS_SECRET and JWT_REFRESH_SECRET must differ")
	}

	for key, d := range map[string]time.Duration{
		"SHUTDOWN_TIMEOUT":         c.ShutdownTimeout,
		"JWT_ACCESS_TTL":           c.JWT.AccessTTL,
		"JWT_REFRESH_TTL":          c.JWT.RefreshTTL,
		"AUCTION_PAYMENT_DEADLINE": c.Market.AuctionPaymentDeadline,
		"PAYMENT_PENDING_GRACE":    c.Market.PaymentPendingGrace,
		"ANTI_SNIPE_WINDOW":        c.Market.AntiSnipeWindow,
		"ANTI_SNIPE_EXTENSION":     c.Market.AntiSnipeExtension,
		"ORDER_REQUEST_TTL":        c.Market.OrderRequestTTL,
		"RECENT_WRITE_TTL":         c.Cache.RecentWriteTTL,
		"FEED_CACHE_TTL":           c.Cache.FeedTTL,
	} {
		if d <= 0 {
			r.fail("%s must be positive", key)
		}
	}
	if c.DB.MaxConns < 1 {
		r.fail("DB_MAX_CONNS must be >= 1")
	}
	if c.Market.MaxPaymentDefaults < 1 {
		r.fail("MAX_PAYMENT_DEFAULTS must be >= 1")
	}

	switch c.Payments.Provider {
	case ProviderMock:
		if c.Env == EnvProduction {
			r.fail("PAYMENT_PROVIDER=mock is not allowed in production")
		}
		if c.Payments.MockWebhookSecret == "" {
			r.fail("MOCK_WEBHOOK_SECRET is required when PAYMENT_PROVIDER=mock")
		}
	case ProviderUPILink:
		if c.Payments.PlatformUPIID == "" {
			r.fail("PLATFORM_UPI_ID is required when PAYMENT_PROVIDER=upi_link")
		}
	default:
		r.fail("PAYMENT_PROVIDER must be one of mock, upi_link")
	}

	if c.Env == EnvProduction && !c.Cloudinary.Enabled() {
		r.fail("CLOUDINARY_CLOUD_NAME, CLOUDINARY_API_KEY and CLOUDINARY_API_SECRET are required in production")
	}
}

// reader collects every problem instead of stopping at the first, so one run reports them all.
type reader struct {
	get  func(string) string
	errs []error
}

func (r *reader) fail(format string, args ...any) {
	r.errs = append(r.errs, fmt.Errorf(format, args...))
}

func (r *reader) str(key, def string) string {
	if v := strings.TrimSpace(r.get(key)); v != "" {
		return v
	}
	return def
}

func (r *reader) required(key string) string {
	v := strings.TrimSpace(r.get(key))
	if v == "" {
		r.fail("%s is required", key)
	}
	return v
}

func (r *reader) int(key string, def int) int {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		r.fail("%s: %q is not an integer", key, v)
		return def
	}
	return n
}

func (r *reader) bool(key string, def bool) bool {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.fail("%s: %q is not a boolean", key, v)
		return def
	}
	return b
}

func (r *reader) duration(key string, def time.Duration) time.Duration {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.fail("%s: %q is not a duration (e.g. 30s, 15m, 48h)", key, v)
		return def
	}
	return d
}

func (r *reader) list(key string) []string {
	var out []string
	for _, part := range strings.Split(r.str(key, ""), ",") {
		if p := strings.ToLower(strings.TrimSpace(part)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (r *reader) percent(key, def string) int64 {
	bps, err := money.ParsePercent(r.str(key, def))
	if err != nil {
		r.fail("%s: %v", key, err)
	}
	return bps
}

func (r *reader) logLevel(key string, def slog.Level) slog.Level {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(v)); err != nil {
		r.fail("%s: %q is not a log level (debug, info, warn, error)", key, v)
		return def
	}
	return lvl
}

// limit parses "<requests>/<window>", e.g. "120/1m" or "5/15m".
func (r *reader) limit(key string, def Limit) Limit {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	n, w, ok := strings.Cut(v, "/")
	reqs, err1 := strconv.Atoi(strings.TrimSpace(n))
	win, err2 := time.ParseDuration(strings.TrimSpace(w))
	if !ok || err1 != nil || err2 != nil || reqs < 1 || win <= 0 {
		r.fail("%s: %q must look like 120/1m", key, v)
		return def
	}
	return Limit{Requests: reqs, Window: win}
}
