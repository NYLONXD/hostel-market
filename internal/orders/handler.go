package orders

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/reqctx"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers order endpoints; the caller must require authentication.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/listings/{id}/orders", httpx.Handle(h.request))
	r.Get("/orders", httpx.Handle(h.list))
	r.Get("/orders/{id}", httpx.Handle(h.get))
	r.Post("/orders/{id}/accept", httpx.Handle(h.act(h.svc.Accept)))
	r.Post("/orders/{id}/reject", httpx.Handle(h.withReason(h.svc.Reject)))
	r.Post("/orders/{id}/cancel", httpx.Handle(h.withReason(h.svc.Cancel)))
	r.Post("/orders/{id}/payment-method", httpx.Handle(h.paymentMethod))
	r.Post("/orders/{id}/confirm-payment", httpx.Handle(h.act(h.svc.ConfirmPayment)))
	r.Post("/orders/{id}/handover", httpx.Handle(h.act(h.svc.HandOver)))
	r.Post("/orders/{id}/complete", httpx.Handle(h.act(h.svc.Complete)))
}

type OrderView struct {
	ID                  uuid.UUID            `json:"id"`
	ListingID           uuid.UUID            `json:"listing_id"`
	BuyerID             uuid.UUID            `json:"buyer_id"`
	SellerID            uuid.UUID            `json:"seller_id"`
	Kind                Kind                 `json:"kind"`
	Status              Status               `json:"status"`
	PricePaise          int64                `json:"price_paise"`
	PlatformFeeBps      int32                `json:"platform_fee_bps"`
	PlatformFeePaise    int64                `json:"platform_fee_paise"`
	SellerPayoutPaise   int64                `json:"seller_payout_paise"`
	PaymentMethod       *PaymentMethod       `json:"payment_method"`
	BuyerNote           *string              `json:"buyer_note,omitempty"`
	RequestExpiresAt    *time.Time           `json:"request_expires_at,omitempty"`
	PaymentDueAt        *time.Time           `json:"payment_due_at,omitempty"`
	AcceptedAt          *time.Time           `json:"accepted_at,omitempty"`
	PaidAt              *time.Time           `json:"paid_at,omitempty"`
	HandedOverAt        *time.Time           `json:"handed_over_at,omitempty"`
	CompletedAt         *time.Time           `json:"completed_at,omitempty"`
	ClosedAt            *time.Time           `json:"closed_at,omitempty"`
	CloseReason         *string              `json:"close_reason,omitempty"`
	PaymentInstructions *PaymentInstructions `json:"payment_instructions,omitempty"`
	CreatedAt           time.Time            `json:"created_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
}

func NewOrderView(o Order) OrderView {
	return OrderView{
		ID: o.ID, ListingID: o.ListingID, BuyerID: o.BuyerID, SellerID: o.SellerID, Kind: o.Kind, Status: o.Status,
		PricePaise: o.PricePaise, PlatformFeeBps: o.PlatformFeeBps, PlatformFeePaise: o.PlatformFeePaise,
		SellerPayoutPaise: o.SellerPayoutPaise, PaymentMethod: o.PaymentMethod, BuyerNote: o.BuyerNote,
		RequestExpiresAt: o.RequestExpiresAt, PaymentDueAt: o.PaymentDueAt, AcceptedAt: o.AcceptedAt, PaidAt: o.PaidAt,
		HandedOverAt: o.HandedOverAt, CompletedAt: o.CompletedAt, ClosedAt: o.ClosedAt, CloseReason: o.CloseReason,
		CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
}

type requestBody struct {
	Note *string `json:"note" validate:"omitnil,max=500"`
}

func (h *Handler) request(w http.ResponseWriter, r *http.Request) error {
	listingID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("listing not found")
	}
	var body requestBody
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(w, r, &body); err != nil {
			return err
		}
	}
	uid, _ := reqctx.UserID(r.Context())
	o, err := h.svc.Request(r.Context(), uid, listingID, body.Note)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, NewOrderView(o))
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return errNotFound
	}
	uid, _ := reqctx.UserID(r.Context())
	o, err := h.svc.Get(r.Context(), uid, id)
	if err != nil {
		return err
	}
	return h.writeOrder(w, r, http.StatusOK, o)
}

// writeOrder adds payment instructions for the buyer of a fixed-price order awaiting payment.
func (h *Handler) writeOrder(w http.ResponseWriter, r *http.Request, status int, o Order) error {
	v := NewOrderView(o)
	if uid, _ := reqctx.UserID(r.Context()); uid == o.BuyerID {
		pi, err := h.svc.DirectPaymentInstructions(r.Context(), o)
		if err != nil {
			return err
		}
		v.PaymentInstructions = pi
	}
	httpx.WriteJSON(w, status, v)
	return nil
}

type listResponse struct {
	Items      []OrderView `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	qv := r.URL.Query()
	f := ListFilter{AsBuyer: true, AsSeller: true, Limit: 20}
	switch qv.Get("role") {
	case "", "all":
	case "buyer":
		f.AsSeller = false
	case "seller":
		f.AsBuyer = false
	default:
		return queryErr("role", "must be buyer, seller or all")
	}
	if s := qv.Get("status"); s != "" {
		st := Status(s)
		if !slices.Contains(AllStatuses, st) {
			return queryErr("status", "unknown status")
		}
		f.Status = &st
	}
	if s := qv.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 50 {
			return queryErr("limit", "must be between 1 and 50")
		}
		f.Limit = int32(n)
	}
	if s := qv.Get("cursor"); s != "" {
		var c Cursor
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil || json.Unmarshal(b, &c) != nil {
			return httpx.BadRequest("invalid_cursor", "cursor is invalid")
		}
		f.Cursor = &c
	}

	uid, _ := reqctx.UserID(r.Context())
	orders, next, err := h.svc.List(r.Context(), uid, f)
	if err != nil {
		return err
	}
	resp := listResponse{Items: make([]OrderView, len(orders))}
	for i, o := range orders {
		resp.Items[i] = NewOrderView(o)
	}
	if next != nil {
		b, _ := json.Marshal(next)
		resp.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
	return nil
}

type paymentMethodBody struct {
	Method PaymentMethod `json:"method" validate:"required,oneof=upi_direct cash"`
}

func (h *Handler) paymentMethod(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return errNotFound
	}
	var body paymentMethodBody
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		return err
	}
	uid, _ := reqctx.UserID(r.Context())
	o, err := h.svc.ChoosePaymentMethod(r.Context(), uid, id, body.Method)
	if err != nil {
		return err
	}
	return h.writeOrder(w, r, http.StatusOK, o)
}

type actFunc func(ctx context.Context, actorID, orderID uuid.UUID) (Order, error)

func (h *Handler) act(fn actFunc) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			return errNotFound
		}
		uid, _ := reqctx.UserID(r.Context())
		o, err := fn(r.Context(), uid, id)
		if err != nil {
			return err
		}
		return h.writeOrder(w, r, http.StatusOK, o)
	}
}

type reasonBody struct {
	Reason *string `json:"reason" validate:"omitnil,max=300"`
}

func (h *Handler) withReason(fn func(ctx context.Context, actorID, orderID uuid.UUID, reason *string) (Order, error)) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			return errNotFound
		}
		var body reasonBody
		if r.ContentLength != 0 {
			if err := httpx.DecodeJSON(w, r, &body); err != nil {
				return err
			}
		}
		uid, _ := reqctx.UserID(r.Context())
		o, err := fn(r.Context(), uid, id, body.Reason)
		if err != nil {
			return err
		}
		return h.writeOrder(w, r, http.StatusOK, o)
	}
}

func queryErr(field, msg string) error {
	return httpx.ValidationFailed(httpx.FieldError{Field: field, Rule: "query", Message: msg})
}
