package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"hostelmarket/internal/config"
	"hostelmarket/internal/platform/clock"
)

const (
	typeAccess  = "access"
	typeRefresh = "refresh"
	audience    = "hostelmarket-api"
	leeway      = 5 * time.Second
)

var errWrongTokenType = errors.New("wrong token type")

// AccessClaims ride on short-lived access tokens. Role is a hint for clients only;
// admin routes re-check the role in the database.
type AccessClaims struct {
	jwt.RegisteredClaims
	Type string `json:"typ"`
	Role string `json:"role"`
}

// RefreshClaims identify one row in refresh_tokens (ID = jti) and its rotation family.
type RefreshClaims struct {
	jwt.RegisteredClaims
	Type     string `json:"typ"`
	FamilyID string `json:"fid"`
}

// Tokens signs and verifies JWTs. Access and refresh tokens use different secrets, so
// one can never be replayed as the other even before the typ check.
type Tokens struct {
	cfg   config.JWTConfig
	clock clock.Clock
}

func NewTokens(cfg config.JWTConfig, clk clock.Clock) *Tokens {
	return &Tokens{cfg: cfg, clock: clk}
}

func (t *Tokens) NewAccess(userID uuid.UUID, role string) (string, AccessClaims, error) {
	now := t.clock.Now()
	c := AccessClaims{
		RegisteredClaims: t.registered(userID, now, t.cfg.AccessTTL),
		Type:             typeAccess,
		Role:             role,
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.cfg.AccessSecret)
	if err != nil {
		return "", AccessClaims{}, fmt.Errorf("sign access token: %w", err)
	}
	return s, c, nil
}

func (t *Tokens) NewRefresh(userID, familyID uuid.UUID) (string, RefreshClaims, error) {
	now := t.clock.Now()
	c := RefreshClaims{
		RegisteredClaims: t.registered(userID, now, t.cfg.RefreshTTL),
		Type:             typeRefresh,
		FamilyID:         familyID.String(),
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.cfg.RefreshSecret)
	if err != nil {
		return "", RefreshClaims{}, fmt.Errorf("sign refresh token: %w", err)
	}
	return s, c, nil
}

func (t *Tokens) ParseAccess(raw string) (AccessClaims, error) {
	var c AccessClaims
	if err := t.parse(raw, &c, t.cfg.AccessSecret); err != nil {
		return AccessClaims{}, err
	}
	if c.Type != typeAccess {
		return AccessClaims{}, errWrongTokenType
	}
	return c, nil
}

func (t *Tokens) ParseRefresh(raw string) (RefreshClaims, error) {
	var c RefreshClaims
	if err := t.parse(raw, &c, t.cfg.RefreshSecret); err != nil {
		return RefreshClaims{}, err
	}
	if c.Type != typeRefresh {
		return RefreshClaims{}, errWrongTokenType
	}
	return c, nil
}

func (t *Tokens) parse(raw string, claims jwt.Claims, secret []byte) error {
	_, err := jwt.ParseWithClaims(raw, claims,
		func(*jwt.Token) (any, error) { return secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(t.cfg.Issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(leeway),
		jwt.WithTimeFunc(t.clock.Now),
	)
	return err
}

func (t *Tokens) registered(userID uuid.UUID, now time.Time, ttl time.Duration) jwt.RegisteredClaims {
	return jwt.RegisteredClaims{
		ID:        uuid.NewString(),
		Subject:   userID.String(),
		Issuer:    t.cfg.Issuer,
		Audience:  jwt.ClaimStrings{audience},
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}
}
