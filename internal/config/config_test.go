package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func baseEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL_PRIMARY": "postgres://localhost:5432/hostel_market",
		"REDIS_URL":            "redis://localhost:6379/0",
		"JWT_ACCESS_SECRET":    strings.Repeat("a", 32),
		"JWT_REFRESH_SECRET":   strings.Repeat("b", 32),
		"MOCK_WEBHOOK_SECRET":  "whsec",
	}
}

func loadMap(env map[string]string) (Config, error) {
	return load(func(k string) string { return env[k] })
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := loadMap(baseEnv())
	require.NoError(t, err)

	assert.Equal(t, EnvDevelopment, cfg.Env)
	assert.Empty(t, cfg.DB.ReplicaURL, "replica falls back to primary at the pool layer")
	assert.EqualValues(t, 2000, cfg.Market.PlatformFeeBps)
	assert.Equal(t, 30*time.Minute, cfg.Market.AuctionPaymentDeadline)
	assert.Equal(t, 30*time.Second, cfg.Market.AntiSnipeWindow)
	assert.Equal(t, 60*time.Second, cfg.Market.AntiSnipeExtension)
	assert.Equal(t, 10*time.Second, cfg.Cache.RecentWriteTTL)
	assert.Equal(t, Limit{120, time.Minute}, cfg.RateLimit.Global)
	assert.Equal(t, ProviderMock, cfg.Payments.Provider)
	assert.False(t, cfg.Cloudinary.Enabled())
}

func TestLoadOverrides(t *testing.T) {
	env := baseEnv()
	env["PLATFORM_FEE_PERCENT"] = "12.5"
	env["RATE_LIMIT_BID"] = "3/10s"
	env["COLLEGE_EMAIL_DOMAINS"] = " IITX.ac.in , students.iitx.ac.in,"
	env["DATABASE_URL_REPLICA"] = "postgres://localhost:5433/hostel_market"

	cfg, err := loadMap(env)
	require.NoError(t, err)
	assert.EqualValues(t, 1250, cfg.Market.PlatformFeeBps)
	assert.Equal(t, Limit{3, 10 * time.Second}, cfg.RateLimit.Bid)
	assert.Equal(t, []string{"iitx.ac.in", "students.iitx.ac.in"}, cfg.Auth.CollegeEmailDomains)
	assert.Equal(t, "postgres://localhost:5433/hostel_market", cfg.DB.ReplicaURL)
}

func TestLoadReportsAllErrors(t *testing.T) {
	env := map[string]string{
		"APP_ENV":              "staging",
		"JWT_ACCESS_SECRET":    "short",
		"PLATFORM_FEE_PERCENT": "120",
		"RATE_LIMIT_LOGIN":     "five per minute",
		"PAYMENT_PROVIDER":     "upi_link",
	}
	_, err := loadMap(env)
	require.Error(t, err)
	msg := err.Error()
	for _, want := range []string{
		"APP_ENV", "DATABASE_URL_PRIMARY is required", "REDIS_URL is required",
		"JWT_ACCESS_SECRET must be at least", "JWT_REFRESH_SECRET is required",
		"PLATFORM_FEE_PERCENT", "RATE_LIMIT_LOGIN", "PLATFORM_UPI_ID is required",
	} {
		assert.Contains(t, msg, want)
	}
}

func TestProductionRules(t *testing.T) {
	env := baseEnv()
	env["APP_ENV"] = EnvProduction
	_, err := loadMap(env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PAYMENT_PROVIDER=mock is not allowed in production")
	assert.Contains(t, err.Error(), "CLOUDINARY_CLOUD_NAME")
}

func TestParseDotEnvLine(t *testing.T) {
	tests := []struct {
		line    string
		key     string
		val     string
		ok      bool
		wantErr bool
	}{
		{line: "", ok: false},
		{line: "# comment", ok: false},
		{line: "A=1", key: "A", val: "1", ok: true},
		{line: "export B = two ", key: "B", val: "two", ok: true},
		{line: `C="quoted # not a comment"`, key: "C", val: "quoted # not a comment", ok: true},
		{line: "D='x=y'", key: "D", val: "x=y", ok: true},
		{line: "E=value # trailing comment", key: "E", val: "value", ok: true},
		{line: "F=postgres://u:p@h:5432/db?sslmode=disable", key: "F", val: "postgres://u:p@h:5432/db?sslmode=disable", ok: true},
		{line: "G=", key: "G", val: "", ok: true},
		{line: "no equals sign", wantErr: true},
	}
	for _, tc := range tests {
		key, val, ok, err := parseDotEnvLine(tc.line)
		if tc.wantErr {
			assert.Error(t, err, tc.line)
			continue
		}
		require.NoError(t, err, tc.line)
		assert.Equal(t, tc.ok, ok, tc.line)
		assert.Equal(t, tc.key, key, tc.line)
		assert.Equal(t, tc.val, val, tc.line)
	}
}
