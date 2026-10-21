// Package admin exposes the operator endpoints: payouts, payment verification, user
// verification and listing takedown. It owns no data; it composes the other modules
// through small interfaces. Every route requires the admin role (checked in the DB).
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"hostelmarket/internal/listings"
	"hostelmarket/internal/payments"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/reqctx"
	"hostelmarket/internal/users"
)

type Users interface {
	ListUnverified(ctx context.Context, after *users.UnverifiedCursor, limit int32) ([]users.User, error)
	SetVerified(ctx context.Context, id uuid.UUID, verified bool) (users.User, error)
	ResetPaymentDefaults(ctx context.Context, id uuid.UUID) (users.User, error)
}

type Listings interface {
	Remove(ctx context.Context, actorID, id uuid.UUID, asAdmin bool) (listings.Listing, error)
}

type Payments interface {
	ListPayouts(ctx context.Context, status *string, after *payments.PayoutCursor, limit int32) ([]payments.Payout, *payments.PayoutCursor, error)
	MarkPayoutPaid(ctx context.Context, adminID, ledgerID uuid.UUID, reference string) (payments.Payout, error)
	ListByStatus(ctx context.Context, status payments.Status, after *payments.PaymentCursor, limit int32) ([]payments.Payment, *payments.PaymentCursor, error)
	AdminMarkPaid(ctx context.Context, adminID, paymentID uuid.UUID) (payments.Payment, error)
	AdminMarkFailed(ctx context.Context, adminID, paymentID uuid.UUID, reason string) (payments.Payment, error)
}

type Handler struct {
	users    Users
	listings Listings
	payments Payments
}

func NewHandler(u Users, l Listings, p Payments) *Handler {
	return &Handler{users: u, listings: l, payments: p}
}

// Routes registers /admin endpoints. The caller must apply auth.RequireRole(admin).
func (h *Handler) Routes(r chi.Router) {
	r.Get("/admin/payouts", httpx.Handle(h.listPayouts))
	r.Post("/admin/payouts/{id}/mark-paid", httpx.Handle(h.markPayoutPaid))
	r.Get("/admin/payments", httpx.Handle(h.listPayments))
	r.Post("/admin/payments/{id}/mark-paid", httpx.Handle(h.markPaymentPaid))
	r.Post("/admin/payments/{id}/mark-failed", httpx.Handle(h.markPaymentFailed))
	r.Get("/admin/users", httpx.Handle(h.listUnverified))
	r.Post("/admin/users/{id}/verify", httpx.Handle(h.verifyUser))
	r.Post("/admin/users/{id}/reset-payment-defaults", httpx.Handle(h.resetDefaults))
	r.Post("/admin/listings/{id}/remove", httpx.Handle(h.removeListing))
}

type page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func (h *Handler) listPayouts(w http.ResponseWriter, r *http.Request) error {
	qv := r.URL.Query()
	limit, err := parseLimit(qv.Get("limit"))
	if err != nil {
		return err
	}
	status := strings.TrimSpace(qv.Get("status"))
	if status == "" {
		status = "pending"
	}
	var filter *string
	switch status {
	case "pending", "paid_out":
		filter = &status
	case "all":
	default:
		return queryErr("status", "must be pending, paid_out or all")
	}
	var after payments.PayoutCursor
	has, err := httpx.DecodeCursor(qv.Get("cursor"), &after)
	if err != nil {
		return err
	}
	items, next, err := h.payments.ListPayouts(r.Context(), filter, cursorPtr(has, &after), limit)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, page[payments.Payout]{Items: items, NextCursor: encode(next)})
	return nil
}

type referenceBody struct {
	Reference string `json:"reference" validate:"required,max=100"`
}

func (h *Handler) markPayoutPaid(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("payout not found")
	}
	var body referenceBody
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		return err
	}
	p, err := h.payments.MarkPayoutPaid(r.Context(), adminID(r), id, body.Reference)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, p)
	return nil
}

func (h *Handler) listPayments(w http.ResponseWriter, r *http.Request) error {
	qv := r.URL.Query()
	limit, err := parseLimit(qv.Get("limit"))
	if err != nil {
		return err
	}
	status := payments.Status(qv.Get("status"))
	if status == "" {
		status = payments.StatusPending // UTR submitted, waiting for verification
	}
	switch status {
	case payments.StatusCreated, payments.StatusPending, payments.StatusPaid, payments.StatusFailed, payments.StatusExpired:
	default:
		return queryErr("status", "must be created, pending, paid, failed or expired")
	}
	var after payments.PaymentCursor
	has, err := httpx.DecodeCursor(qv.Get("cursor"), &after)
	if err != nil {
		return err
	}
	items, next, err := h.payments.ListByStatus(r.Context(), status, cursorPtr(has, &after), limit)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, page[payments.Payment]{Items: items, NextCursor: encode(next)})
	return nil
}

func (h *Handler) markPaymentPaid(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("payment not found")
	}
	p, err := h.payments.AdminMarkPaid(r.Context(), adminID(r), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, p)
	return nil
}

type reasonBody struct {
	Reason string `json:"reason" validate:"required,max=300"`
}

func (h *Handler) markPaymentFailed(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("payment not found")
	}
	var body reasonBody
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		return err
	}
	p, err := h.payments.AdminMarkFailed(r.Context(), adminID(r), id, body.Reason)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, p)
	return nil
}

type adminUserView struct {
	users.MeView
}

func (h *Handler) listUnverified(w http.ResponseWriter, r *http.Request) error {
	qv := r.URL.Query()
	limit, err := parseLimit(qv.Get("limit"))
	if err != nil {
		return err
	}
	var after users.UnverifiedCursor
	has, err := httpx.DecodeCursor(qv.Get("cursor"), &after)
	if err != nil {
		return err
	}
	list, err := h.users.ListUnverified(r.Context(), cursorPtr(has, &after), limit+1)
	if err != nil {
		return httpx.Internal(err)
	}
	var next *users.UnverifiedCursor
	if len(list) > int(limit) {
		list = list[:limit]
		last := list[len(list)-1]
		next = &users.UnverifiedCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	items := make([]adminUserView, len(list))
	for i, u := range list {
		items[i] = adminUserView{users.NewMeView(u)}
	}
	httpx.WriteJSON(w, http.StatusOK, page[adminUserView]{Items: items, NextCursor: encode(next)})
	return nil
}

type verifyBody struct {
	Verified *bool `json:"verified" validate:"required"`
}

func (h *Handler) verifyUser(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("user not found")
	}
	var body verifyBody
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		return err
	}
	u, err := h.users.SetVerified(r.Context(), id, *body.Verified)
	if err != nil {
		return userErr(err)
	}
	httpx.WriteJSON(w, http.StatusOK, users.NewMeView(u))
	return nil
}

func (h *Handler) resetDefaults(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("user not found")
	}
	u, err := h.users.ResetPaymentDefaults(r.Context(), id)
	if err != nil {
		return userErr(err)
	}
	httpx.WriteJSON(w, http.StatusOK, users.NewMeView(u))
	return nil
}

func (h *Handler) removeListing(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("listing not found")
	}
	var body reasonBody
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		return err
	}
	l, err := h.listings.Remove(r.Context(), adminID(r), id, true)
	if err != nil {
		return err
	}
	slog.InfoContext(r.Context(), "admin removed listing", "listing_id", id, "reason", body.Reason)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": l.ID, "status": l.Status})
	return nil
}

func adminID(r *http.Request) uuid.UUID {
	id, _ := reqctx.UserID(r.Context())
	return id
}

func userErr(err error) error {
	if errors.Is(err, users.ErrNotFound) {
		return httpx.NotFound("user not found")
	}
	return httpx.Internal(err)
}

func parseLimit(s string) (int32, error) {
	if s == "" {
		return 50, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 200 {
		return 0, queryErr("limit", "must be between 1 and 200")
	}
	return int32(n), nil
}

func cursorPtr[T any](has bool, v *T) *T {
	if !has {
		return nil
	}
	return v
}

func encode[T any](c *T) string {
	if c == nil {
		return ""
	}
	return httpx.EncodeCursor(c)
}

func queryErr(field, msg string) error {
	return httpx.ValidationFailed(httpx.FieldError{Field: field, Rule: "query", Message: msg})
}
