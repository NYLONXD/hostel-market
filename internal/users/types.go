// Package users owns user records: profiles, roles, verification and the
// payment-default flag. Credentials are stored here but checked by package auth.
package users

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleStudent Role = "student"
	RoleAdmin   Role = "admin"
)

var (
	ErrNotFound   = errors.New("users: not found")
	ErrEmailTaken = errors.New("users: email already registered")
)

type User struct {
	ID              uuid.UUID
	Email           string
	Name            string
	Hostel          string
	Block           string
	Year            int16
	UPIID           *string
	Role            Role
	IsVerified      bool
	VerifiedAt      *time.Time
	PaymentDefaults int32
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CanSell: listing items needs a verified account and a UPI ID to get paid on.
func (u User) CanSell() bool { return u.IsVerified && u.UPIID != nil }

type NewUser struct {
	Email        string
	PasswordHash string
	Name         string
	Hostel       string
	Block        string
	Year         int16
	UPIID        *string
	Verified     bool
}

// Credentials is a user plus the stored password hash, for login only.
type Credentials struct {
	User         User
	PasswordHash string
}

// ProfilePatch holds optional updates; nil fields are left unchanged.
type ProfilePatch struct {
	Name     *string
	Block    *string
	Year     *int16
	SetUPIID bool    // true => UPIID replaces the stored value (nil clears it)
	UPIID    *string // only read when SetUPIID
}

// NormalizeEmail lower-cases and trims so lookups and the unique index agree.
func NormalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// NormalizeLabel canonicalises hostel/block names: trimmed, single-spaced, upper-case,
// so "block b " and "Block  B" are the same block for eligibility checks.
func NormalizeLabel(s string) string { return strings.ToUpper(strings.Join(strings.Fields(s), " ")) }
