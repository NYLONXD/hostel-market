package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"

	"hostelmarket/internal/listings"
	"hostelmarket/internal/orders"
	"hostelmarket/internal/payments/store"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/jobs"
)

const (
	TaskMockWebhook = "payments:mock_webhook"
	TaskReconcile   = "payments:reconcile"
)

// OrderStore is the slice of the orders module payments needs.
type OrderStore interface {
	LockForPayment(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (orders.Order, listings.Listing, error)
	MarkAuctionPaid(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (orders.Order, error)
	OrderPaid(ctx context.Context, o orders.Order, hostel string)
	Get(ctx context.Context, actorID, orderID uuid.UUID) (orders.Order, error)
}

type Notifier interface {
	PublishToUsers(ctx context.Context, userIDs []uuid.UUID, eventType string, data any)
}

type Options struct {
	DB       *db.Router
	Orders   OrderStore
	Provider Provider
	Jobs     jobs.Enqueuer
	Notify   Notifier // optional
	Clock    clock.Clock
	// PendingGrace is how long past the deadline a pending payment may still settle.
	PendingGrace time.Duration
	// WebhookBaseURL is where the mock provider delivers its webhooks (this API).
	WebhookBaseURL string
}

type Service struct {
	db         *db.Router
	orders     OrderStore
	provider   Provider
	jobs       jobs.Enqueuer
	notify     Notifier
	clock      clock.Clock
	grace      time.Duration
	webhookURL string
}

func NewService(o Options) *Service {
	return &Service{db: o.DB, orders: o.Orders, provider: o.Provider, jobs: o.Jobs, notify: o.Notify,
		clock: o.Clock, grace: o.PendingGrace, webhookURL: strings.TrimRight(o.WebhookBaseURL, "/") + "/v1/payments/webhooks/"}
}

type Payment struct {
	ID            uuid.UUID  `json:"id"`
	OrderID       uuid.UUID  `json:"order_id"`
	PayerID       uuid.UUID  `json:"payer_id"`
	Provider      string     `json:"provider"`
	Status        Status     `json:"status"`
	AmountPaise   int64      `json:"amount_paise"`
	Currency      string     `json:"currency"`
	UPIURI        *string    `json:"upi_uri,omitempty"`
	BuyerUTR      *string    `json:"buyer_utr,omitempty"`
	FailureReason *string    `json:"failure_reason,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	PaidAt        *time.Time `json:"paid_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

var (
	errNotFound   = httpx.NotFound("payment not found")
	idemKeyRegexp = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
	utrRegexp     = regexp.MustCompile(`^[A-Za-z0-9]{6,35}$`)
)

// Create starts (or, for a repeated Idempotency-Key, returns) the platform payment for
// an auction order. The intent is recorded before the provider is called, so a crash
// in between is repaired by simply retrying with the same key.
func (s *Service) Create(ctx context.Context, payerID, orderID uuid.UUID, idemKey string) (Payment, error) {
	if !idemKeyRegexp.MatchString(idemKey) {
		return Payment{}, httpx.BadRequest("idempotency_key_required", "send an Idempotency-Key header of 8-64 letters, digits, '-' or '_'")
	}

	var (
		p             store.Payment
		needsProvider bool
	)
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		q := store.New(tx)
		existing, err := q.GetPaymentByIdempotencyKey(ctx, store.GetPaymentByIdempotencyKeyParams{PayerID: payerID, IdempotencyKey: idemKey})
		switch {
		case err == nil:
			if existing.OrderID != orderID {
				return httpx.NewError(422, "idempotency_key_reused", "this Idempotency-Key was already used for a different order")
			}
			p, needsProvider = existing, existing.ProviderRef == nil && Status(existing.Status) == StatusCreated
			return nil
		case !db.IsNoRows(err):
			return err
		}

		o, _, err := s.orders.LockForPayment(ctx, tx, orderID)
		if err != nil {
			return err
		}
		switch {
		case !o.IsParticipant(payerID):
			return httpx.NotFound("order not found")
		case o.BuyerID != payerID:
			return httpx.Forbidden("wrong_party", "only the buyer pays for an order")
		case o.Kind != orders.KindAuction:
			return httpx.Conflict("direct_payment", "fixed-price orders are paid to the seller directly; see the order's payment_instructions")
		case o.Status != orders.StatusAwaitingPayment:
			return httpx.Conflict("order_not_payable", "this order is "+string(o.Status)+" and can't be paid")
		}

		if live, err := q.GetLivePaymentForOrder(ctx, orderID); err == nil {
			p, needsProvider = live, live.ProviderRef == nil // resume the attempt in progress
			return nil
		} else if !db.IsNoRows(err) {
			return err
		}
		p, err = q.InsertPayment(ctx, store.InsertPaymentParams{
			OrderID: orderID, PayerID: payerID, Provider: s.provider.Name(), AmountPaise: o.PricePaise,
			IdempotencyKey: idemKey, ExpiresAt: o.PaymentDueAt,
		})
		needsProvider = true
		return err
	})
	if db.IsUniqueViolation(err) {
		return Payment{}, httpx.Conflict("payment_in_progress", "a payment for this order is already being created; retry shortly")
	}
	if err != nil {
		return Payment{}, asAPIError(err)
	}
	if !needsProvider {
		return toPayment(p), nil
	}

	// Outside any transaction: no row locks are held across a provider call.
	res, err := s.provider.CreatePayment(ctx, CreateRequest{
		PaymentID: p.ID, OrderID: p.OrderID, AmountPaise: p.AmountPaise,
		Note: "Hostel Market order " + strings.ToUpper(p.OrderID.String()[:8]),
	})
	if err != nil {
		reason := "provider error: " + err.Error()
		_, _ = s.setStatus(ctx, p.ID, StatusFailed, &reason, nil)
		return Payment{}, &httpx.Error{Status: 502, Code: "payment_provider_error", Message: "the payment provider is unavailable, try again", Err: err}
	}
	var uri *string
	if res.UPIURI != "" {
		uri = &res.UPIURI
	}
	err = s.db.WithTx(ctx, func(tx pgx.Tx) error {
		p, err = store.New(tx).SetProviderResult(ctx, store.SetProviderResultParams{ID: p.ID, ProviderRef: &res.ProviderRef, UpiUri: uri, Status: string(res.Status)})
		return err
	})
	if err != nil {
		return Payment{}, httpx.Internal(err)
	}
	return toPayment(p), nil
}

// SubmitUTR records the bank reference a buyer says they paid with. It moves the payment
// to pending (holding off expiry for the grace period) but never marks it paid.
func (s *Service) SubmitUTR(ctx context.Context, payerID, paymentID uuid.UUID, utr string) (Payment, error) {
	utr = strings.ToUpper(strings.TrimSpace(utr))
	if !utrRegexp.MatchString(utr) {
		return Payment{}, httpx.ValidationFailed(httpx.FieldError{Field: "utr", Rule: "format", Message: "must be 6-35 letters or digits"})
	}
	var p store.Payment
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.GetPaymentForUpdate(ctx, paymentID)
		if db.IsNoRows(err) || (err == nil && cur.PayerID != payerID) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if st := Status(cur.Status); st != StatusCreated && st != StatusPending {
			return httpx.Conflict("payment_closed", "this payment is "+cur.Status)
		}
		p, err = q.SetBuyerUTR(ctx, store.SetBuyerUTRParams{ID: paymentID, Utr: utr})
		return err
	})
	if err != nil {
		return Payment{}, asAPIError(err)
	}
	return toPayment(p), nil
}

// Get returns a payment to the participants of its order.
func (s *Service) Get(ctx context.Context, actorID, paymentID uuid.UUID) (Payment, error) {
	p, err := store.New(s.db.Reader(ctx, db.Strong)).GetPayment(ctx, paymentID)
	if db.IsNoRows(err) {
		return Payment{}, errNotFound
	}
	if err != nil {
		return Payment{}, httpx.Internal(err)
	}
	if _, err := s.orders.Get(ctx, actorID, p.OrderID); err != nil {
		return Payment{}, errNotFound
	}
	return toPayment(p), nil
}

func (s *Service) ListForOrder(ctx context.Context, actorID, orderID uuid.UUID) ([]Payment, error) {
	if _, err := s.orders.Get(ctx, actorID, orderID); err != nil {
		return nil, err
	}
	rows, err := store.New(s.db.Reader(ctx, db.Strong)).ListPaymentsForOrder(ctx, orderID)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	out := make([]Payment, len(rows))
	for i, r := range rows {
		out[i] = toPayment(r)
	}
	return out, nil
}

// HandleWebhook verifies and applies a provider notification. Recording the event and
// applying it happen in one transaction: a replay is a no-op, and a failure rolls back
// the record too, so the provider's retry is processed properly.
func (s *Service) HandleWebhook(ctx context.Context, providerName string, h http.Header, body []byte) error {
	if providerName != s.provider.Name() {
		return httpx.NotFound("unknown payment provider")
	}
	ev, err := s.provider.VerifyWebhook(h, body)
	switch {
	case errors.Is(err, ErrWebhooksUnsupported):
		return httpx.NotFound("this provider does not use webhooks")
	case errors.Is(err, ErrBadSignature):
		return httpx.NewError(401, "invalid_signature", "webhook signature verification failed")
	case err != nil:
		return httpx.BadRequest("invalid_webhook", err.Error())
	}

	payload := body
	if !json.Valid(payload) {
		payload, _ = json.Marshal(map[string]string{"raw": string(body)})
	}
	var done *settled
	err = s.db.WithTx(ctx, func(tx pgx.Tx) error {
		q := store.New(tx)
		eventRow, err := q.InsertWebhookEvent(ctx, store.InsertWebhookEventParams{Provider: providerName, EventID: ev.EventID, Payload: payload})
		if db.IsNoRows(err) {
			return nil // already processed
		}
		if err != nil {
			return err
		}
		p, err := q.GetPaymentByProviderRef(ctx, store.GetPaymentByProviderRefParams{Provider: providerName, ProviderRef: &ev.ProviderRef})
		switch {
		case db.IsNoRows(err):
			slog.WarnContext(ctx, "webhook for unknown payment", "provider", providerName, "ref", ev.ProviderRef)
		case err != nil:
			return err
		case ev.Status == StatusPaid && ev.AmountPaise != 0 && ev.AmountPaise != p.AmountPaise:
			slog.ErrorContext(ctx, "webhook amount mismatch; not settling", "payment_id", p.ID, "want", p.AmountPaise, "got", ev.AmountPaise)
		case ev.Status == StatusPaid:
			if done, err = s.settleTx(ctx, tx, p.ID, nil); err != nil {
				return err
			}
		case ev.Status == StatusFailed:
			reason := "provider reported failure"
			if err := s.closeTx(ctx, tx, p.ID, StatusFailed, &reason, nil); err != nil && !isAlreadyClosed(err) {
				return err // a failure notice for an already-settled payment is ignored
			}
		case ev.Status == StatusPending && Status(p.Status) == StatusCreated:
			if _, err := q.SetPaymentStatus(ctx, store.SetPaymentStatusParams{ID: p.ID, Status: string(StatusPending)}); err != nil {
				return err
			}
		}
		return q.MarkWebhookProcessed(ctx, eventRow)
	})
	if err != nil {
		return asAPIError(err)
	}
	s.afterSettle(ctx, done)
	return nil
}

// AdminMarkPaid settles a payment an admin verified by hand (e.g. a UTR on the
// platform's bank statement).
func (s *Service) AdminMarkPaid(ctx context.Context, adminID, paymentID uuid.UUID) (Payment, error) {
	var done *settled
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		done, err = s.settleTx(ctx, tx, paymentID, &adminID)
		return err
	})
	if err != nil {
		return Payment{}, asAPIError(err)
	}
	s.afterSettle(ctx, done)
	return toPayment(done.payment), nil
}

// AdminMarkFailed rejects a payment that couldn't be verified; the buyer can try again
// before the deadline.
func (s *Service) AdminMarkFailed(ctx context.Context, adminID, paymentID uuid.UUID, reason string) (Payment, error) {
	var p store.Payment
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		if err := s.closeTx(ctx, tx, paymentID, StatusFailed, &reason, &adminID); err != nil {
			return err
		}
		var err error
		p, err = store.New(tx).GetPayment(ctx, paymentID)
		return err
	})
	if err != nil {
		return Payment{}, asAPIError(err)
	}
	return toPayment(p), nil
}

type PaymentCursor struct {
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"i"`
}

func (s *Service) ListByStatus(ctx context.Context, status Status, after *PaymentCursor, limit int32) ([]Payment, *PaymentCursor, error) {
	p := store.ListPaymentsByStatusParams{Status: string(status), RowLimit: limit + 1}
	if after != nil {
		p.AfterCreated, p.AfterID = &after.CreatedAt, &after.ID
	}
	rows, err := store.New(s.db.Reader(ctx, db.Strong)).ListPaymentsByStatus(ctx, p)
	if err != nil {
		return nil, nil, httpx.Internal(err)
	}
	out := make([]Payment, 0, len(rows))
	for _, r := range rows {
		out = append(out, toPayment(r))
	}
	var next *PaymentCursor
	if len(out) > int(limit) {
		out = out[:limit]
		last := out[len(out)-1]
		next = &PaymentCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return out, next, nil
}

// --- settlement -------------------------------------------------------------------

type settled struct {
	payment store.Payment
	order   *orders.Order // nil when nothing new was settled
	hostel  string
	late    bool
}

// settleTx marks a payment paid and, if its order is still awaiting payment, moves the
// order to paid and writes the ledger entry (gross, fee, seller payout pending).
// Locks follow the global order: listing, order, payment.
func (s *Service) settleTx(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID, verifiedBy *uuid.UUID) (*settled, error) {
	q := store.New(tx)
	peek, err := q.GetPayment(ctx, paymentID)
	if db.IsNoRows(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	o, l, err := s.orders.LockForPayment(ctx, tx, peek.OrderID)
	if err != nil {
		return nil, err
	}
	p, err := q.GetPaymentForUpdate(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	if Status(p.Status) == StatusPaid {
		return &settled{payment: p}, nil // idempotent
	}

	if o.Status != orders.StatusAwaitingPayment {
		// Money arrived after the order stopped being payable (deadline passed, item
		// re-offered). Record it so an admin can refund; don't touch the order.
		reason := fmt.Sprintf("received after the order became %s; refund required", o.Status)
		p, err = q.SetPaymentStatus(ctx, store.SetPaymentStatusParams{ID: p.ID, Status: string(StatusPaid), FailureReason: &reason, VerifiedBy: verifiedBy})
		return &settled{payment: p, late: true}, err
	}

	p, err = q.SetPaymentStatus(ctx, store.SetPaymentStatusParams{ID: p.ID, Status: string(StatusPaid), VerifiedBy: verifiedBy})
	if err != nil {
		return nil, err
	}
	paidOrder, err := s.orders.MarkAuctionPaid(ctx, tx, o.ID)
	if err != nil {
		return nil, err
	}
	if _, err := q.InsertLedgerEntry(ctx, store.InsertLedgerEntryParams{
		OrderID: paidOrder.ID, PaymentID: p.ID, SellerID: paidOrder.SellerID, GrossPaise: paidOrder.PricePaise,
		PlatformFeePaise: paidOrder.PlatformFeePaise, SellerPayoutPaise: paidOrder.SellerPayoutPaise,
	}); err != nil {
		return nil, err
	}
	return &settled{payment: p, order: &paidOrder, hostel: l.Hostel}, nil
}

func (s *Service) afterSettle(ctx context.Context, d *settled) {
	if d == nil {
		return
	}
	if d.late {
		slog.ErrorContext(ctx, "late payment received; refund required", "payment_id", d.payment.ID, "order_id", d.payment.OrderID)
		return
	}
	if d.order == nil {
		return
	}
	s.orders.OrderPaid(ctx, *d.order, d.hostel)
	if s.notify != nil {
		s.notify.PublishToUsers(ctx, []uuid.UUID{d.order.BuyerID, d.order.SellerID}, "payment.paid", map[string]any{
			"order_id": d.order.ID, "payment_id": d.payment.ID, "amount_paise": d.payment.AmountPaise,
		})
	}
}

// closeTx moves a created/pending payment to failed or expired.
func (s *Service) closeTx(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID, to Status, reason *string, by *uuid.UUID) error {
	q := store.New(tx)
	peek, err := q.GetPayment(ctx, paymentID)
	if db.IsNoRows(err) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	if _, _, err := s.orders.LockForPayment(ctx, tx, peek.OrderID); err != nil {
		return err
	}
	p, err := q.GetPaymentForUpdate(ctx, paymentID)
	if err != nil {
		return err
	}
	if st := Status(p.Status); st != StatusCreated && st != StatusPending {
		return httpx.Conflict("payment_closed", "this payment is already "+p.Status)
	}
	_, err = q.SetPaymentStatus(ctx, store.SetPaymentStatusParams{ID: paymentID, Status: string(to), FailureReason: reason, VerifiedBy: by})
	return err
}

func (s *Service) setStatus(ctx context.Context, id uuid.UUID, to Status, reason *string, by *uuid.UUID) (store.Payment, error) {
	var p store.Payment
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		p, err = store.New(tx).SetPaymentStatus(ctx, store.SetPaymentStatusParams{ID: id, Status: string(to), FailureReason: reason, VerifiedBy: by})
		return err
	})
	return p, err
}

// --- orders.PaymentGuard -----------------------------------------------------------

// HoldExpiry keeps an order alive past its deadline while a payment is pending (a UTR
// was submitted, or the gateway reported pending), up to the grace period, and asks the
// provider for a status in the meantime.
func (s *Service) HoldExpiry(ctx context.Context, tx pgx.Tx, o orders.Order) (*time.Time, error) {
	live, err := store.New(tx).GetLivePaymentForOrder(ctx, o.ID)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if Status(live.Status) != StatusPending || o.PaymentDueAt == nil {
		return nil, nil
	}
	until := o.PaymentDueAt.Add(s.grace)
	if !s.clock.Now().Before(until) {
		return nil, nil
	}
	s.enqueueReconcile(ctx, live.ID)
	return &until, nil
}

func (s *Service) ExpirePayments(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) error {
	return store.New(tx).ExpireOpenPayments(ctx, orderID)
}

// --- background tasks ----------------------------------------------------------------

type paymentPayload struct {
	PaymentID uuid.UUID `json:"payment_id"`
	Outcome   Status    `json:"outcome,omitempty"`
}

func (s *Service) enqueueReconcile(ctx context.Context, paymentID uuid.UUID) {
	b, _ := json.Marshal(paymentPayload{PaymentID: paymentID})
	if err := s.jobs.Enqueue(context.WithoutCancel(ctx), asynq.NewTask(TaskReconcile, b), asynq.MaxRetry(5), asynq.Unique(time.Minute)); err != nil {
		slog.WarnContext(ctx, "enqueue payment reconcile failed", "payment_id", paymentID, "err", err)
	}
}

// HandleReconcile asks the provider for a payment's status and applies a final answer.
func (s *Service) HandleReconcile(ctx context.Context, t *asynq.Task) error {
	var in paymentPayload
	if err := json.Unmarshal(t.Payload(), &in); err != nil {
		return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
	}
	p, err := store.New(s.db.Reader(ctx, db.Strong)).GetPayment(ctx, in.PaymentID)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err // retried by asynq
	}
	if p.ProviderRef == nil {
		return nil
	}
	st, err := s.provider.GetStatus(ctx, *p.ProviderRef)
	if errors.Is(err, ErrStatusUnsupported) {
		return nil // e.g. UPI intents: an admin verifies these
	}
	if err != nil {
		return err
	}
	switch st {
	case StatusPaid:
		var done *settled
		err = s.db.WithTx(ctx, func(tx pgx.Tx) error {
			done, err = s.settleTx(ctx, tx, p.ID, nil)
			return err
		})
		if err == nil {
			s.afterSettle(ctx, done)
		}
		return err
	case StatusFailed:
		reason := "provider reported failure"
		err = s.db.WithTx(ctx, func(tx pgx.Tx) error { return s.closeTx(ctx, tx, p.ID, StatusFailed, &reason, nil) })
		if isAlreadyClosed(err) {
			return nil
		}
		return err
	}
	return nil
}

// Simulate (development only) makes the mock gateway call our webhook after delay.
func (s *Service) Simulate(ctx context.Context, actorID, paymentID uuid.UUID, outcome Status, delay time.Duration) error {
	if _, ok := s.provider.(*MockProvider); !ok {
		return httpx.NotFound("simulation is only available with PAYMENT_PROVIDER=mock")
	}
	if outcome != StatusPaid && outcome != StatusFailed && outcome != StatusPending {
		return httpx.ValidationFailed(httpx.FieldError{Field: "outcome", Rule: "oneof", Message: "must be success, failure or pending"})
	}
	p, err := store.New(s.db.Reader(ctx, db.Strong)).GetPayment(ctx, paymentID)
	if db.IsNoRows(err) || (err == nil && p.PayerID != actorID) {
		return errNotFound
	}
	if err != nil {
		return httpx.Internal(err)
	}
	if p.ProviderRef == nil {
		return httpx.Conflict("payment_not_started", "this payment has no provider reference yet")
	}
	b, _ := json.Marshal(paymentPayload{PaymentID: p.ID, Outcome: outcome})
	return s.jobs.Enqueue(ctx, asynq.NewTask(TaskMockWebhook, b), asynq.ProcessIn(delay), asynq.MaxRetry(3))
}

func (s *Service) HandleMockWebhook(ctx context.Context, t *asynq.Task) error {
	mock, ok := s.provider.(*MockProvider)
	if !ok {
		return nil
	}
	var in paymentPayload
	if err := json.Unmarshal(t.Payload(), &in); err != nil {
		return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
	}
	p, err := store.New(s.db.Reader(ctx, db.Strong)).GetPayment(ctx, in.PaymentID)
	if err != nil || p.ProviderRef == nil {
		return err
	}
	return mock.deliver(ctx, s.webhookURL+mock.Name(), *p.ProviderRef, in.Outcome, p.AmountPaise)
}

func (s *Service) RegisterJobs(w jobs.Registry) {
	w.Handle(TaskReconcile, s.HandleReconcile)
	w.Handle(TaskMockWebhook, s.HandleMockWebhook)
}

func toPayment(r store.Payment) Payment {
	return Payment{
		ID: r.ID, OrderID: r.OrderID, PayerID: r.PayerID, Provider: r.Provider, Status: Status(r.Status),
		AmountPaise: r.AmountPaise, Currency: r.Currency, UPIURI: r.UpiUri, BuyerUTR: r.BuyerUtr,
		FailureReason: r.FailureReason, ExpiresAt: r.ExpiresAt, PaidAt: r.PaidAt, CreatedAt: r.CreatedAt,
	}
}

func isAlreadyClosed(err error) bool {
	var apiErr *httpx.Error
	return errors.As(err, &apiErr) && apiErr.Code == "payment_closed"
}

func asAPIError(err error) error {
	var apiErr *httpx.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return httpx.Internal(err)
}
