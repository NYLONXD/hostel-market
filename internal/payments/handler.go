package payments

import (
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/reqctx"
)

const maxWebhookBytes = 64 << 10

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers authenticated payment endpoints.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/orders/{id}/payments", httpx.Handle(h.create))
	r.Get("/orders/{id}/payments", httpx.Handle(h.listForOrder))
	r.Get("/payments/{id}", httpx.Handle(h.get))
	r.Post("/payments/{id}/utr", httpx.Handle(h.utr))
}

// WebhookRoutes registers the public provider callback (authenticated by signature).
func (h *Handler) WebhookRoutes(r chi.Router) {
	r.Post("/payments/webhooks/{provider}", httpx.Handle(h.webhook))
}

// DevRoutes registers the mock-payment simulator; mount only in development.
func (h *Handler) DevRoutes(r chi.Router) {
	r.Post("/dev/payments/{id}/simulate", httpx.Handle(h.simulate))
}

// PaymentView adds the next step the client should take.
type PaymentView struct {
	Payment
	NextStep string `json:"next_step,omitempty"`
}

func view(p Payment) PaymentView {
	v := PaymentView{Payment: p}
	if p.Status == StatusCreated || p.Status == StatusPending {
		switch p.Provider {
		case "upi_link":
			v.NextStep = "open upi_uri in any UPI app and pay; then POST /v1/payments/" + p.ID.String() + "/utr with the UTR so an admin can verify it"
		case "mock":
			v.NextStep = "POST /v1/dev/payments/" + p.ID.String() + `/simulate with {"outcome":"success"} or {"outcome":"failure"}`
		}
	}
	return v
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	orderID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("order not found")
	}
	uid, _ := reqctx.UserID(r.Context())
	p, err := h.svc.Create(r.Context(), uid, orderID, r.Header.Get("Idempotency-Key"))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, view(p))
	return nil
}

func (h *Handler) listForOrder(w http.ResponseWriter, r *http.Request) error {
	orderID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("order not found")
	}
	uid, _ := reqctx.UserID(r.Context())
	ps, err := h.svc.ListForOrder(r.Context(), uid, orderID)
	if err != nil {
		return err
	}
	items := make([]PaymentView, len(ps))
	for i, p := range ps {
		items[i] = view(p)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return errNotFound
	}
	uid, _ := reqctx.UserID(r.Context())
	p, err := h.svc.Get(r.Context(), uid, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, view(p))
	return nil
}

type utrBody struct {
	UTR string `json:"utr" validate:"required,max=40"`
}

func (h *Handler) utr(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return errNotFound
	}
	var body utrBody
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		return err
	}
	uid, _ := reqctx.UserID(r.Context())
	p, err := h.svc.SubmitUTR(r.Context(), uid, id, body.UTR)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, view(p))
	return nil
}

func (h *Handler) webhook(w http.ResponseWriter, r *http.Request) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	if err != nil {
		return httpx.BadRequest("invalid_webhook", "could not read body")
	}
	if err := h.svc.HandleWebhook(r.Context(), chi.URLParam(r, "provider"), r.Header, body); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	return nil
}

type simulateBody struct {
	Outcome      string `json:"outcome" validate:"required,oneof=success failure pending"`
	DelaySeconds int    `json:"delay_seconds" validate:"gte=0,lte=600"`
}

func (h *Handler) simulate(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return errNotFound
	}
	var body simulateBody
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		return err
	}
	outcome := map[string]Status{"success": StatusPaid, "failure": StatusFailed, "pending": StatusPending}[body.Outcome]
	uid, _ := reqctx.UserID(r.Context())
	if err := h.svc.Simulate(r.Context(), uid, id, outcome, time.Duration(body.DelaySeconds)*time.Second); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
		"status": "scheduled", "outcome": body.Outcome, "delay_seconds": body.DelaySeconds,
	})
	return nil
}
