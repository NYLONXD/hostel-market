// Package auth handles registration, login, JWT access/refresh tokens with rotation
// and reuse detection, logout, and the authentication/authorization middleware.
package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"hostelmarket/internal/auth/store"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/users"
)

// UserStore is the slice of the users module that auth needs.
type UserStore interface {
	Create(ctx context.Context, nu users.NewUser) (users.User, error)
	Credentials(ctx context.Context, email string) (users.Credentials, error)
	Get(ctx context.Context, id uuid.UUID) (users.User, error)
}

type Options struct {
	Users               UserStore
	DB                  *db.Router
	Tokens              *Tokens
	Blocklist           Blocklist
	Clock               clock.Clock
	CollegeEmailDomains []string
	BcryptCost          int // 0 => DefaultBcryptCost
}

const DefaultBcryptCost = 12

type Service struct {
	users     UserStore
	db        *db.Router
	tokens    *Tokens
	blocklist Blocklist
	clock     clock.Clock
	domains   []string
	cost      int
	dummyHash []byte
}

func NewService(o Options) (*Service, error) {
	cost := o.BcryptCost
	if cost == 0 {
		cost = DefaultBcryptCost
	}
	// Compared against on unknown emails so login timing doesn't reveal which emails exist.
	dummy, err := bcrypt.GenerateFromPassword([]byte("not-a-real-password"), cost)
	if err != nil {
		return nil, fmt.Errorf("auth: prepare dummy hash: %w", err)
	}
	return &Service{
		users: o.Users, db: o.DB, tokens: o.Tokens, blocklist: o.Blocklist, clock: o.Clock,
		domains: o.CollegeEmailDomains, cost: cost, dummyHash: dummy,
	}, nil
}

// Session is what register/login/refresh hand back to the client.
type Session struct {
	User             users.User
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time

	refreshJTI uuid.UUID
	familyID   uuid.UUID
}

// Principal is the authenticated caller of a request.
type Principal struct {
	UserID    uuid.UUID
	Role      users.Role
	TokenID   string
	ExpiresAt time.Time
}

var (
	errInvalidCredentials = httpx.Unauthorized("invalid email or password")
	errInvalidToken       = httpx.NewError(401, "invalid_token", "access token is invalid or expired")
	errRevokedToken       = httpx.NewError(401, "invalid_token", "access token has been revoked")
	errInvalidRefresh     = httpx.NewError(401, "invalid_refresh_token", "refresh token is invalid or expired")
	errRefreshReuse       = httpx.NewError(401, "invalid_refresh_token", "refresh token was already used; all sessions in this chain were signed out")
)

type RegisterInput struct {
	Email    string
	Password string
	Name     string
	Hostel   string
	Block    string
	Year     int16
	UPIID    *string
}

func (s *Service) Register(ctx context.Context, in RegisterInput) (Session, error) {
	email := users.NormalizeEmail(in.Email)
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.cost)
	if err != nil {
		return Session{}, httpx.Internal(fmt.Errorf("hash password: %w", err))
	}
	u, err := s.users.Create(ctx, users.NewUser{
		Email:        email,
		PasswordHash: string(hash),
		Name:         strings.TrimSpace(in.Name),
		Hostel:       in.Hostel,
		Block:        in.Block,
		Year:         in.Year,
		UPIID:        in.UPIID,
		Verified:     s.isCollegeEmail(email),
	})
	if errors.Is(err, users.ErrEmailTaken) {
		return Session{}, httpx.Conflict("email_taken", "an account with this email already exists")
	}
	if err != nil {
		return Session{}, httpx.Internal(err)
	}
	return s.startSession(ctx, u)
}

func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	creds, err := s.users.Credentials(ctx, email)
	if errors.Is(err, users.ErrNotFound) {
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
		return Session{}, errInvalidCredentials
	}
	if err != nil {
		return Session{}, httpx.Internal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(creds.PasswordHash), []byte(password)) != nil {
		return Session{}, errInvalidCredentials
	}
	return s.startSession(ctx, creds.User)
}

// Refresh rotates a refresh token: the presented token is marked used and a new one in
// the same family is issued. Presenting an already-used token means it leaked, so the
// whole family is revoked and every device in that chain must log in again.
func (s *Service) Refresh(ctx context.Context, raw string) (Session, error) {
	claims, err := s.tokens.ParseRefresh(raw)
	if err != nil {
		return Session{}, errInvalidRefresh
	}
	jti, err1 := uuid.Parse(claims.ID)
	userID, err2 := uuid.Parse(claims.Subject)
	familyID, err3 := uuid.Parse(claims.FamilyID)
	if err := errors.Join(err1, err2, err3); err != nil {
		return Session{}, errInvalidRefresh
	}

	u, err := s.users.Get(ctx, userID)
	if errors.Is(err, users.ErrNotFound) {
		return Session{}, errInvalidRefresh
	}
	if err != nil {
		return Session{}, httpx.Internal(err)
	}

	next, err := s.newSession(u, familyID)
	if err != nil {
		return Session{}, err
	}

	var reused bool
	err = s.db.WithTx(ctx, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.GetRefreshTokenForUpdate(ctx, jti)
		if db.IsNoRows(err) {
			return errInvalidRefresh
		}
		if err != nil {
			return err
		}
		switch {
		case row.RevokedAt != nil, row.UserID != userID, !row.ExpiresAt.After(s.clock.Now()):
			return errInvalidRefresh
		case row.UsedAt != nil:
			reused = true
			return q.RevokeRefreshFamily(ctx, row.FamilyID) // commit the revocation
		}
		if err := q.MarkRefreshTokenUsed(ctx, jti); err != nil {
			return err
		}
		return storeRefresh(ctx, q, next)
	})
	switch {
	case reused:
		return Session{}, errRefreshReuse
	case err != nil:
		return Session{}, asAPIError(err)
	}
	return next, nil
}

// Logout revokes the current access token (via the Redis blocklist) and, when given,
// the refresh token's whole family.
func (s *Service) Logout(ctx context.Context, p Principal, refreshRaw string) error {
	if err := s.blocklist.Block(ctx, p.TokenID, p.ExpiresAt); err != nil {
		return httpx.Internal(fmt.Errorf("blocklist access token: %w", err))
	}
	familyID, ok := s.ownedRefreshFamily(p, refreshRaw)
	if !ok {
		return nil // absent, expired or someone else's token: nothing of ours to revoke
	}
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		return store.New(tx).RevokeRefreshFamily(ctx, familyID)
	})
	if err != nil {
		return httpx.Internal(fmt.Errorf("revoke refresh family: %w", err))
	}
	return nil
}

func (s *Service) ownedRefreshFamily(p Principal, raw string) (uuid.UUID, bool) {
	if raw == "" {
		return uuid.Nil, false
	}
	claims, err := s.tokens.ParseRefresh(raw)
	if err != nil || claims.Subject != p.UserID.String() {
		return uuid.Nil, false
	}
	familyID, err := uuid.Parse(claims.FamilyID)
	return familyID, err == nil
}

// Authenticate verifies an access token and checks it hasn't been logged out.
// If the blocklist can't be consulted we refuse (503) rather than accept a
// possibly-revoked token.
func (s *Service) Authenticate(ctx context.Context, raw string) (Principal, error) {
	c, err := s.tokens.ParseAccess(raw)
	if err != nil {
		return Principal{}, errInvalidToken
	}
	uid, err := uuid.Parse(c.Subject)
	if err != nil {
		return Principal{}, errInvalidToken
	}
	blocked, err := s.blocklist.IsBlocked(ctx, c.ID)
	if err != nil {
		return Principal{}, &httpx.Error{Status: 503, Code: "auth_unavailable", Message: "authentication is temporarily unavailable", Err: err}
	}
	if blocked {
		return Principal{}, errRevokedToken
	}
	return Principal{UserID: uid, Role: users.Role(c.Role), TokenID: c.ID, ExpiresAt: c.ExpiresAt.Time}, nil
}

func (s *Service) startSession(ctx context.Context, u users.User) (Session, error) {
	sess, err := s.newSession(u, uuid.New())
	if err != nil {
		return Session{}, err
	}
	err = s.db.WithTx(ctx, func(tx pgx.Tx) error {
		return storeRefresh(ctx, store.New(tx), sess)
	})
	if err != nil {
		return Session{}, httpx.Internal(fmt.Errorf("store refresh token: %w", err))
	}
	return sess, nil
}

func (s *Service) newSession(u users.User, familyID uuid.UUID) (Session, error) {
	access, ac, err := s.tokens.NewAccess(u.ID, string(u.Role))
	if err != nil {
		return Session{}, httpx.Internal(err)
	}
	refresh, rc, err := s.tokens.NewRefresh(u.ID, familyID)
	if err != nil {
		return Session{}, httpx.Internal(err)
	}
	jti, err := uuid.Parse(rc.ID)
	if err != nil {
		return Session{}, httpx.Internal(fmt.Errorf("refresh jti: %w", err))
	}
	return Session{
		User:             u,
		AccessToken:      access,
		AccessExpiresAt:  ac.ExpiresAt.Time,
		RefreshToken:     refresh,
		RefreshExpiresAt: rc.ExpiresAt.Time,
		refreshJTI:       jti,
		familyID:         familyID,
	}, nil
}

func storeRefresh(ctx context.Context, q *store.Queries, sess Session) error {
	return q.InsertRefreshToken(ctx, store.InsertRefreshTokenParams{
		Jti:       sess.refreshJTI,
		UserID:    sess.User.ID,
		FamilyID:  sess.familyID,
		ExpiresAt: sess.RefreshExpiresAt,
	})
}

func (s *Service) isCollegeEmail(email string) bool {
	_, domain, ok := strings.Cut(email, "@")
	return ok && slices.Contains(s.domains, domain)
}

func asAPIError(err error) error {
	var apiErr *httpx.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return httpx.Internal(err)
}
