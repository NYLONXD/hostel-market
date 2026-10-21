package users

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/users/store"
)

type Service struct {
	db *db.Router
}

func NewService(router *db.Router) *Service { return &Service{db: router} }

// Create inserts a user. Returns ErrEmailTaken on a duplicate email.
func (s *Service) Create(ctx context.Context, nu NewUser) (User, error) {
	var row store.User
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		row, err = store.New(tx).CreateUser(ctx, store.CreateUserParams{
			Email:        NormalizeEmail(nu.Email),
			PasswordHash: nu.PasswordHash,
			Name:         nu.Name,
			Hostel:       NormalizeLabel(nu.Hostel),
			Block:        NormalizeLabel(nu.Block),
			Year:         nu.Year,
			UpiID:        nu.UPIID,
			IsVerified:   nu.Verified,
		})
		return err
	})
	if db.IsUniqueViolation(err) {
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return fromRow(row), nil
}

// Credentials looks up a login. Auth data always comes from the primary.
func (s *Service) Credentials(ctx context.Context, email string) (Credentials, error) {
	row, err := store.New(s.db.Reader(ctx, db.Strong)).GetUserByEmail(ctx, NormalizeEmail(email))
	if db.IsNoRows(err) {
		return Credentials{}, ErrNotFound
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("get user by email: %w", err)
	}
	return Credentials{User: fromRow(row), PasswordHash: row.PasswordHash}, nil
}

// Get reads from the primary. Use it for anything that gates an action
// (role, verification, eligibility).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (User, error) {
	return s.get(ctx, id, db.Strong)
}

// GetForDisplay may read from the replica; for showing profiles only.
func (s *Service) GetForDisplay(ctx context.Context, id uuid.UUID) (User, error) {
	return s.get(ctx, id, db.ReplicaOK)
}

func (s *Service) get(ctx context.Context, id uuid.UUID, c db.Consistency) (User, error) {
	row, err := store.New(s.db.Reader(ctx, c)).GetUserByID(ctx, id)
	if db.IsNoRows(err) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	return fromRow(row), nil
}

func (s *Service) UpdateProfile(ctx context.Context, id uuid.UUID, p ProfilePatch) (User, error) {
	if p.Block != nil {
		b := NormalizeLabel(*p.Block)
		p.Block = &b
	}
	return s.write(ctx, func(q *store.Queries) (store.User, error) {
		return q.UpdateProfile(ctx, store.UpdateProfileParams{
			ID:       id,
			Name:     p.Name,
			Block:    p.Block,
			Year:     p.Year,
			SetUpiID: p.SetUPIID,
			UpiID:    p.UPIID,
		})
	})
}

func (s *Service) SetVerified(ctx context.Context, id uuid.UUID, verified bool) (User, error) {
	return s.write(ctx, func(q *store.Queries) (store.User, error) {
		return q.SetVerified(ctx, store.SetVerifiedParams{ID: id, Verified: verified})
	})
}

func (s *Service) ResetPaymentDefaults(ctx context.Context, id uuid.UUID) (User, error) {
	return s.write(ctx, func(q *store.Queries) (store.User, error) {
		return q.ResetPaymentDefaults(ctx, id)
	})
}

// IncrementPaymentDefaults flags a buyer who let an auction payment expire. It runs
// inside the caller's transaction so the flag and the order expiry commit together.
func (s *Service) IncrementPaymentDefaults(ctx context.Context, q db.DBTX, id uuid.UUID) (int32, error) {
	n, err := store.New(q).IncrementPaymentDefaults(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("increment payment defaults: %w", err)
	}
	return n, nil
}

// UnverifiedCursor pages the admin verification queue.
type UnverifiedCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

func (s *Service) ListUnverified(ctx context.Context, after *UnverifiedCursor, limit int32) ([]User, error) {
	params := store.ListUnverifiedUsersParams{RowLimit: limit}
	if after != nil {
		params.AfterCreated, params.AfterID = &after.CreatedAt, &after.ID
	}
	rows, err := store.New(s.db.Reader(ctx, db.Strong)).ListUnverifiedUsers(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list unverified users: %w", err)
	}
	out := make([]User, len(rows))
	for i, r := range rows {
		out[i] = fromRow(r)
	}
	return out, nil
}

func (s *Service) write(ctx context.Context, fn func(q *store.Queries) (store.User, error)) (User, error) {
	var row store.User
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		row, err = fn(store.New(tx))
		return err
	})
	if db.IsNoRows(err) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("update user: %w", err)
	}
	return fromRow(row), nil
}

func fromRow(r store.User) User {
	return User{
		ID:              r.ID,
		Email:           r.Email,
		Name:            r.Name,
		Hostel:          r.Hostel,
		Block:           r.Block,
		Year:            r.Year,
		UPIID:           r.UpiID,
		Role:            Role(r.Role),
		IsVerified:      r.IsVerified,
		VerifiedAt:      r.VerifiedAt,
		PaymentDefaults: r.PaymentDefaults,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}
