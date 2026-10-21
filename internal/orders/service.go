package orders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"

	"hostelmarket/internal/listings"
	"hostelmarket/internal/orders/store"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/jobs"
	"hostelmarket/internal/users"
)

// ListingStore is the slice of the listings module orders needs.
type ListingStore interface {
	Lock(ctx context.Context, tx db.DBTX, id uuid.UUID) (listings.Listing, error)
	SetStatus(ctx context.Context, tx db.DBTX, id uuid.UUID, status listings.Status) (listings.Listing, error)
	FeedChanged(ctx context.Context, hostel string)
	GetStrong(ctx context.Context, id uuid.UUID) (listings.Listing, error)
}

type UserStore interface {
	Get(ctx context.Context, id uuid.UUID) (users.User, error)
	IncrementPaymentDefaults(ctx context.Context, q db.DBTX, id uuid.UUID) (int32, error)
}

// Notifier pushes realtime events to users (WebSocket). Optional.
type Notifier interface {
	PublishToUsers(ctx context.Context, userIDs []uuid.UUID, eventType string, data any)
}

const (
	TaskExpireRequest = "orders:expire_request"
	TaskSweep         = "orders:sweep"
)

type Service struct {
	db         *db.Router
	listings   ListingStore
	users      UserStore
	jobs       jobs.Enqueuer
	notify     Notifier
	clock      clock.Clock
	requestTTL time.Duration

	bidders  Bidders
	auction  AuctionConfig
	payments PaymentGuard
}

func NewService(router *db.Router, l ListingStore, u UserStore, enq jobs.Enqueuer, n Notifier, clk clock.Clock, requestTTL time.Duration) *Service {
	return &Service{db: router, listings: l, users: u, jobs: enq, notify: n, clock: clk, requestTTL: requestTTL}
}

var (
	errNotFound   = httpx.NotFound("order not found")
	errReserved   = httpx.Conflict("listing_reserved", "this item is already reserved for another buyer")
	errOnlyBuyer  = httpx.Forbidden("wrong_party", "only the buyer can do this")
	errOnlySeller = httpx.Forbidden("wrong_party", "only the seller can do this")
)

// Request creates a purchase request on a fixed-price listing. Every eligibility rule
// is checked against primary data while the listing row is locked.
func (s *Service) Request(ctx context.Context, buyerID, listingID uuid.UUID, note *string) (Order, error) {
	buyer, err := s.users.Get(ctx, buyerID)
	if err != nil {
		return Order{}, httpx.Internal(err)
	}
	if note != nil {
		n := strings.TrimSpace(*note)
		note = &n
	}

	var o Order
	err = s.db.WithTx(ctx, func(tx pgx.Tx) error {
		l, err := s.listings.Lock(ctx, tx, listingID)
		if errors.Is(err, listings.ErrNotFound) {
			return httpx.NotFound("listing not found")
		}
		if err != nil {
			return err
		}
		if l.BiddingEnabled {
			return httpx.Conflict("auction_listing", "this listing is an auction; place a bid instead")
		}
		now := s.clock.Now()
		if err := l.CheckBuyer(listings.BuyerFrom(buyer), now); err != nil {
			return err
		}
		expires := now.Add(s.requestTTL)
		row, err := store.New(tx).InsertOrder(ctx, store.InsertOrderParams{
			ListingID:         l.ID,
			BuyerID:           buyerID,
			SellerID:          l.SellerID,
			Kind:              string(KindFixed),
			Status:            string(StatusRequested),
			PricePaise:        *l.PricePaise,
			SellerPayoutPaise: *l.PricePaise, // fixed price: 0% fee
			BuyerNote:         note,
			RequestExpiresAt:  &expires,
		})
		if err != nil {
			return err
		}
		o = fromRow(row)
		return nil
	})
	if db.IsUniqueViolation(err, "orders_one_request_per_buyer") {
		return Order{}, httpx.Conflict("already_requested", "you already have a pending request for this item")
	}
	if err != nil {
		return Order{}, asAPIError(err)
	}

	s.scheduleRequestExpiry(ctx, o)
	s.publish(ctx, o, "order.requested")
	return o, nil
}

// Accept reserves the item for this buyer. The listing row lock plus the
// orders_one_live_per_listing index guarantee only one buyer can ever get it.
func (s *Service) Accept(ctx context.Context, sellerID, orderID uuid.UUID) (Order, error) {
	return s.transition(ctx, sellerID, orderID, step{
		actor: roleSeller, from: []Status{StatusRequested}, to: StatusAccepted, verb: "accept",
		check: func(o Order, l listings.Listing) error {
			if o.RequestExpiresAt != nil && !s.clock.Now().Before(*o.RequestExpiresAt) {
				return httpx.Conflict("request_expired", "this request has expired")
			}
			switch l.Status {
			case listings.StatusActive:
				return nil
			case listings.StatusReserved:
				return errReserved
			default:
				return httpx.Conflict("listing_unavailable", "this item is no longer available")
			}
		},
		effect: func(ctx context.Context, tx pgx.Tx, _ Order, l listings.Listing) (bool, error) {
			_, err := s.listings.SetStatus(ctx, tx, l.ID, listings.StatusReserved)
			return true, err
		},
	})
}

func (s *Service) Reject(ctx context.Context, sellerID, orderID uuid.UUID, reason *string) (Order, error) {
	return s.transition(ctx, sellerID, orderID, step{
		actor: roleSeller, from: []Status{StatusRequested}, to: StatusRejected, verb: "reject", reason: reason,
	})
}

// Cancel: the buyer may withdraw a request; either party may cancel a fixed-price
// order before payment is confirmed. Cancelling a live order frees the item again.
func (s *Service) Cancel(ctx context.Context, actorID, orderID uuid.UUID, reason *string) (Order, error) {
	return s.transition(ctx, actorID, orderID, step{
		actor: roleEither, from: []Status{StatusRequested, StatusAccepted, StatusAwaitingPayment}, to: StatusCancelled,
		verb: "cancel", reason: reason,
		check: func(o Order, _ listings.Listing) error {
			if o.Kind == KindAuction {
				return httpx.Conflict("auction_order", "auction orders can't be cancelled; unpaid ones expire at the payment deadline")
			}
			if o.Status == StatusRequested && actorID != o.BuyerID {
				return httpx.Conflict("use_reject", "sellers reject requests instead of cancelling them")
			}
			return nil
		},
		effect: s.releaseIfLive,
	})
}

// ChoosePaymentMethod: the buyer picks UPI-to-seller or cash for an accepted fixed-price order.
func (s *Service) ChoosePaymentMethod(ctx context.Context, buyerID, orderID uuid.UUID, m PaymentMethod) (Order, error) {
	if m != MethodUPIDirect && m != MethodCash {
		return Order{}, httpx.ValidationFailed(httpx.FieldError{Field: "method", Rule: "oneof", Message: "must be upi_direct or cash"})
	}
	return s.transition(ctx, buyerID, orderID, step{
		actor: roleBuyer, from: []Status{StatusAccepted}, to: StatusAwaitingPayment, verb: "choose a payment method for",
		method: &m, check: fixedOnly,
	})
}

// ConfirmPayment: the seller confirms they received the money (UPI or cash). The
// platform never touched it, so there is no fee and no ledger entry.
func (s *Service) ConfirmPayment(ctx context.Context, sellerID, orderID uuid.UUID) (Order, error) {
	return s.transition(ctx, sellerID, orderID, step{
		actor: roleSeller, from: []Status{StatusAwaitingPayment}, to: StatusPaid, verb: "confirm payment for",
		check:  fixedOnly,
		effect: s.markSold,
	})
}

func (s *Service) HandOver(ctx context.Context, sellerID, orderID uuid.UUID) (Order, error) {
	return s.transition(ctx, sellerID, orderID, step{
		actor: roleSeller, from: []Status{StatusPaid}, to: StatusHandedOver, verb: "hand over",
	})
}

func (s *Service) Complete(ctx context.Context, buyerID, orderID uuid.UUID) (Order, error) {
	return s.transition(ctx, buyerID, orderID, step{
		actor: roleBuyer, from: []Status{StatusHandedOver}, to: StatusCompleted, verb: "complete",
	})
}

// ExpireRequest is the idempotent job handler body: it expires a request whose deadline
// passed and does nothing otherwise.
func (s *Service) ExpireRequest(ctx context.Context, orderID uuid.UUID) error {
	reason := "seller did not respond in time"
	_, err := s.transition(ctx, uuid.Nil, orderID, step{
		actor: roleSystem, from: []Status{StatusRequested}, to: StatusExpired, verb: "expire", reason: &reason,
		check: func(o Order, _ listings.Listing) error {
			if o.RequestExpiresAt == nil || s.clock.Now().Before(*o.RequestExpiresAt) {
				return errNotDue
			}
			return nil
		},
	})
	if errors.Is(err, errNotDue) || isConflict(err) || errors.Is(err, errNotFound) {
		return nil // already handled, or not due yet
	}
	return err
}

var errNotDue = errors.New("orders: not due")

// --- transactional hooks for other modules -------------------------------------

// ListingRemoved rejects open requests when a listing is taken down (listings.RemovalHook).
func (s *Service) ListingRemoved(ctx context.Context, tx pgx.Tx, l listings.Listing) error {
	_, err := store.New(tx).RejectOpenRequests(ctx, store.RejectOpenRequestsParams{ListingID: l.ID, Reason: "listing removed"})
	return err
}

// --- reads ----------------------------------------------------------------------

// Get returns an order to one of its participants. Orders are always read from the primary.
func (s *Service) Get(ctx context.Context, actorID, orderID uuid.UUID) (Order, error) {
	row, err := store.New(s.db.Reader(ctx, db.Strong)).GetOrder(ctx, orderID)
	if db.IsNoRows(err) {
		return Order{}, errNotFound
	}
	if err != nil {
		return Order{}, httpx.Internal(err)
	}
	o := fromRow(row)
	if !o.IsParticipant(actorID) {
		return Order{}, errNotFound
	}
	return o, nil
}

type ListFilter struct {
	AsBuyer  bool
	AsSeller bool
	Status   *Status
	Cursor   *Cursor
	Limit    int32
}

type Cursor struct {
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"i"`
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]Order, *Cursor, error) {
	p := store.ListOrdersForUserParams{UserID: userID, AsBuyer: f.AsBuyer, AsSeller: f.AsSeller, RowLimit: f.Limit + 1}
	if f.Status != nil {
		st := string(*f.Status)
		p.Status = &st
	}
	if f.Cursor != nil {
		p.AfterCreated, p.AfterID = &f.Cursor.CreatedAt, &f.Cursor.ID
	}
	rows, err := store.New(s.db.Reader(ctx, db.Strong)).ListOrdersForUser(ctx, p)
	if err != nil {
		return nil, nil, httpx.Internal(err)
	}
	out := make([]Order, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	var next *Cursor
	if len(out) > int(f.Limit) {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return out, next, nil
}

// --- transition engine -----------------------------------------------------------

type role int

const (
	roleBuyer role = iota + 1
	roleSeller
	roleEither
	roleSystem
)

type step struct {
	actor  role
	from   []Status
	to     Status
	verb   string
	method *PaymentMethod
	reason *string
	// check runs under the locks, before the status change.
	check func(o Order, l listings.Listing) error
	// effect runs after the status change, in the same transaction; it reports
	// whether the hostel feed changed.
	effect func(ctx context.Context, tx pgx.Tx, o Order, l listings.Listing) (bool, error)
}

// transition applies one state-machine step. Locks are always taken listing first,
// then order, so concurrent transitions on the same item can't deadlock.
func (s *Service) transition(ctx context.Context, actorID, orderID uuid.UUID, st step) (Order, error) {
	var (
		out         Order
		hostel      string
		feedChanged bool
	)
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		q := store.New(tx)
		peek, err := q.GetOrder(ctx, orderID)
		if db.IsNoRows(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if err := authorize(fromRow(peek), actorID, st.actor); err != nil {
			return err
		}

		l, err := s.listings.Lock(ctx, tx, peek.ListingID)
		if err != nil {
			return err
		}
		row, err := q.GetOrderForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		o := fromRow(row)
		if !slices.Contains(st.from, o.Status) || !CanTransition(o.Status, st.to) {
			return httpx.Conflict("invalid_transition", fmt.Sprintf("can't %s an order that is %s", st.verb, o.Status))
		}
		if st.check != nil {
			if err := st.check(o, l); err != nil {
				return err
			}
		}

		params := store.TransitionOrderParams{ID: o.ID, FromStatus: string(o.Status), ToStatus: string(st.to), CloseReason: st.reason}
		if st.method != nil {
			m := string(*st.method)
			params.PaymentMethod = &m
		}
		row, err = q.TransitionOrder(ctx, params)
		if err != nil {
			return err
		}
		out = fromRow(row)
		if st.effect != nil {
			if feedChanged, err = st.effect(ctx, tx, out, l); err != nil {
				return err
			}
		}
		hostel = l.Hostel
		return nil
	})
	if db.IsUniqueViolation(err, "orders_one_live_per_listing") {
		return Order{}, errReserved
	}
	if err != nil {
		return Order{}, asAPIError(err)
	}
	if feedChanged {
		s.listings.FeedChanged(ctx, hostel)
	}
	s.publish(ctx, out, "order.updated")
	return out, nil
}

func authorize(o Order, actor uuid.UUID, r role) error {
	if r == roleSystem {
		return nil
	}
	if !o.IsParticipant(actor) {
		return errNotFound // don't reveal other people's orders
	}
	switch {
	case r == roleBuyer && actor != o.BuyerID:
		return errOnlyBuyer
	case r == roleSeller && actor != o.SellerID:
		return errOnlySeller
	}
	return nil
}

func fixedOnly(o Order, _ listings.Listing) error {
	if o.Kind != KindFixed {
		return httpx.Conflict("auction_order", "auction orders are paid to the platform, not directly")
	}
	return nil
}

// releaseIfLive puts a reserved item back on sale when its live order ends.
func (s *Service) releaseIfLive(ctx context.Context, tx pgx.Tx, o Order, l listings.Listing) (bool, error) {
	if l.Status != listings.StatusReserved {
		return false, nil // the order was only a request; the item wasn't held
	}
	if _, err := s.listings.SetStatus(ctx, tx, l.ID, listings.StatusActive); err != nil {
		return false, err
	}
	return true, nil
}

// markSold finalises the listing and turns away everyone still waiting.
func (s *Service) markSold(ctx context.Context, tx pgx.Tx, o Order, l listings.Listing) (bool, error) {
	if _, err := s.listings.SetStatus(ctx, tx, l.ID, listings.StatusSold); err != nil {
		return false, err
	}
	_, err := store.New(tx).RejectOpenRequests(ctx, store.RejectOpenRequestsParams{ListingID: l.ID, Reason: "item sold"})
	return true, err
}

// --- jobs -------------------------------------------------------------------------

type orderPayload struct {
	OrderID uuid.UUID `json:"order_id"`
}

func (s *Service) scheduleRequestExpiry(ctx context.Context, o Order) {
	if o.RequestExpiresAt == nil {
		return
	}
	b, _ := json.Marshal(orderPayload{OrderID: o.ID})
	err := s.jobs.Enqueue(ctx, asynq.NewTask(TaskExpireRequest, b),
		asynq.ProcessAt(*o.RequestExpiresAt), asynq.TaskID("expire-request:"+o.ID.String()), asynq.MaxRetry(5))
	if err != nil {
		// The periodic sweep expires overdue requests anyway.
		slog.ErrorContext(ctx, "schedule request expiry failed", "order_id", o.ID, "err", err)
	}
}

func (s *Service) HandleExpireRequest(ctx context.Context, t *asynq.Task) error {
	var p orderPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
	}
	return s.ExpireRequest(ctx, p.OrderID)
}

// HandleSweep is the safety net for lost delayed tasks: it expires every overdue
// request and every overdue unpaid auction order.
func (s *Service) HandleSweep(ctx context.Context, _ *asynq.Task) error {
	q := store.New(s.db.Reader(ctx, db.Strong))
	ids, err := q.ListOverdueRequests(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.ExpireRequest(ctx, id); err != nil {
			slog.ErrorContext(ctx, "sweep: expire request failed", "order_id", id, "err", err)
		}
	}
	if s.bidders == nil {
		return nil
	}
	ids, err = q.ListOverduePayments(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.ExpireAuctionPayment(ctx, id); err != nil {
			slog.ErrorContext(ctx, "sweep: expire payment failed", "order_id", id, "err", err)
		}
	}
	return nil
}

func (s *Service) RegisterJobs(w jobs.Registry) error {
	w.Handle(TaskExpireRequest, s.HandleExpireRequest)
	w.Handle(TaskExpirePayment, s.HandleExpirePayment)
	w.Handle(TaskSweep, s.HandleSweep)
	return w.Every("@every 2m", asynq.NewTask(TaskSweep, nil), asynq.Unique(time.Minute))
}

// --- helpers ------------------------------------------------------------------------

func (s *Service) publish(ctx context.Context, o Order, event string) {
	if s.notify != nil {
		s.notify.PublishToUsers(ctx, []uuid.UUID{o.BuyerID, o.SellerID}, event, map[string]any{
			"order_id": o.ID, "listing_id": o.ListingID, "status": o.Status,
		})
	}
}

func fromRow(r store.Order) Order {
	o := Order{
		ID: r.ID, ListingID: r.ListingID, BuyerID: r.BuyerID, SellerID: r.SellerID,
		Kind: Kind(r.Kind), Status: Status(r.Status), PricePaise: r.PricePaise,
		PlatformFeeBps: r.PlatformFeeBps, PlatformFeePaise: r.PlatformFeePaise, SellerPayoutPaise: r.SellerPayoutPaise,
		AuctionRound: r.AuctionRound, WinningBidID: r.WinningBidID, BuyerNote: r.BuyerNote,
		RequestExpiresAt: r.RequestExpiresAt, PaymentDueAt: r.PaymentDueAt, AcceptedAt: r.AcceptedAt,
		PaidAt: r.PaidAt, HandedOverAt: r.HandedOverAt, CompletedAt: r.CompletedAt, ClosedAt: r.ClosedAt,
		CloseReason: r.CloseReason, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.PaymentMethod != nil {
		m := PaymentMethod(*r.PaymentMethod)
		o.PaymentMethod = &m
	}
	return o
}

func isConflict(err error) bool {
	var apiErr *httpx.Error
	return errors.As(err, &apiErr) && apiErr.Status == 409
}

func asAPIError(err error) error {
	if errors.Is(err, errNotDue) {
		return err
	}
	var apiErr *httpx.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return httpx.Internal(err)
}
