package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/reqctx"
	"hostelmarket/internal/users"
)

type principalKey struct{}

// PrincipalFrom returns the authenticated caller, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// TokenSource extracts a raw access token from a request ("" if absent).
type TokenSource func(r *http.Request) string

// FromHeader reads "Authorization: Bearer <token>".
func FromHeader(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// FromHeaderOrQuery also accepts ?access_token=, for WebSocket clients (browsers can't
// set headers on the upgrade). The access log redacts it.
func FromHeaderOrQuery(r *http.Request) string {
	if t := FromHeader(r); t != "" {
		return t
	}
	return r.URL.Query().Get("access_token")
}

type Authenticator interface {
	Authenticate(ctx context.Context, raw string) (Principal, error)
}

// Identify attaches the caller when a token is present. A present-but-bad token is
// rejected right away; a missing token continues anonymously (RequireUser decides).
func Identify(a Authenticator, src TokenSource) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := src(r)
			if raw == "" {
				next.ServeHTTP(w, r)
				return
			}
			p, err := a.Authenticate(r.Context(), raw)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			ctx := context.WithValue(r.Context(), principalKey{}, p)
			ctx = reqctx.SetUserID(ctx, p.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireUser rejects anonymous requests.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := PrincipalFrom(r.Context()); !ok {
			httpx.WriteError(w, r, httpx.Unauthorized("authentication required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RoleLookup fetches the current user record from the primary.
type RoleLookup interface {
	Get(ctx context.Context, id uuid.UUID) (users.User, error)
}

// RequireRole allows only users whose *current* role matches. The JWT role claim can be
// up to one access-TTL stale, so the database is the authority here.
func RequireRole(lookup RoleLookup, role users.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				httpx.WriteError(w, r, httpx.Unauthorized("authentication required"))
				return
			}
			u, err := lookup.Get(r.Context(), p.UserID)
			switch {
			case errors.Is(err, users.ErrNotFound):
				httpx.WriteError(w, r, httpx.Unauthorized("account no longer exists"))
				return
			case err != nil:
				httpx.WriteError(w, r, httpx.Internal(err))
				return
			case u.Role != role:
				httpx.WriteError(w, r, httpx.Forbidden("forbidden", "this action requires the "+string(role)+" role"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
