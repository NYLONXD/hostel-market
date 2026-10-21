// Package reqctx carries request-scoped identity (request ID, acting user) through
// context.Context so platform code can read it without importing domain packages.
package reqctx

import (
	"context"

	"github.com/google/uuid"
)

type ctxKey struct{}

// Info is owned by a single request. It is mutable on purpose: the auth middleware
// fills in UserID deep in the chain and the outer access-log middleware still sees it.
type Info struct {
	RequestID string
	ClientIP  string
	UserID    uuid.UUID // uuid.Nil when anonymous
}

// New attaches a fresh Info to ctx.
func New(ctx context.Context, requestID string) (context.Context, *Info) {
	info := &Info{RequestID: requestID}
	return context.WithValue(ctx, ctxKey{}, info), info
}

// From returns the request Info, or nil outside a request (e.g. background jobs).
func From(ctx context.Context) *Info {
	info, _ := ctx.Value(ctxKey{}).(*Info)
	return info
}

func RequestID(ctx context.Context) string {
	if info := From(ctx); info != nil {
		return info.RequestID
	}
	return ""
}

func ClientIP(ctx context.Context) string {
	if info := From(ctx); info != nil {
		return info.ClientIP
	}
	return ""
}

// UserID returns the authenticated user, if any.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	if info := From(ctx); info != nil && info.UserID != uuid.Nil {
		return info.UserID, true
	}
	return uuid.Nil, false
}

// SetUserID records the acting user. Inside a request it updates the shared Info;
// outside one (jobs, tests) it attaches a new Info.
func SetUserID(ctx context.Context, id uuid.UUID) context.Context {
	if info := From(ctx); info != nil {
		info.UserID = id
		return ctx
	}
	ctx, info := New(ctx, "")
	info.UserID = id
	return ctx
}
