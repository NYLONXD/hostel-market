package orders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"

	"hostelmarket/internal/listings"
	"hostelmarket/internal/orders/store"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/money"
)

const TaskExpirePayment = "orders:expire_payment"

// Candidate is one bidder's best bid in a round (from the bidding module).
type Candidate struct {
	BidID       int64
	BidderID    uuid.UUID
	AmountPaise int64
}

// Bidders ranks a round's bidders, best first. Implemented by package bidding.
type Bidders interface {
	RankedBidders(ctx context.Context, q db.DBTX, listingID uuid.UUID, round int32) ([]Candidate, error)
}

// PaymentGuard lets the payments module hold off expiry while a payment is being
// verified, and expire open payments with their order. Optional.
type PaymentGuard interface {
	// HoldExpiry returns a later time to re-check, or nil to expire now.
	HoldExpiry(ctx context.Context, tx pgx.Tx, o Order) (*time.Time, error)
	ExpirePayments(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) error
}

type AuctionConfig struct {
	FeeBps             int64
	PaymentDeadline    time.Duration
	MaxPaymentDefaults int32
}

// EnableAuctions wires the auction side (done once at startup).
func (s *Service) EnableAuctions(b Bidders, cfg AuctionConfig) {
	s.bidders, s.auction = b, cfg
}

// SetPaymentGuard is called by the payments module during wiring.
func (s *Service) SetPaymentGuard(g PaymentGuard) { s.payments = g }

// AwardAuction offers the item to the best remaining bidder of the round: someone who
// hasn't already let a win expire here and isn't blocked for repeated defaults. It
// creates an awaiting_payment order whose fee and payout are fixed now, and reserves
// the listing. Returns nil when nobody is left. The caller holds the listing lock.
func (s *Service) AwardAuction(ctx context.Context, tx pgx.Tx, l listings.Listing) (*Order, error) {
	if l.Auction == nil {
		return nil, errors.New("orders: award on a non-auction listing")
	}
	round := l.Auction.Round
	q := store.New(tx)

	cands, err := s.bidders.RankedBidders(ctx, tx, l.ID, round)
	if err != nil {
		return nil, err
	}
	defaulted, err := q.ListDefaultedBuyers(ctx, store.ListDefaultedBuyersParams{ListingID: l.ID, AuctionRound: &round})
	if err != nil {
		return nil, err
	}
	skip := make(map[uuid.UUID]bool, len(defaulted))
	for _, id := range defaulted {
		skip[id] = true
	}

	for _, c := range cands {
		if skip[c.BidderID] {
			continue
		}
		u, err := s.users.Get(ctx, c.BidderID)
		if err != nil {
			return nil, err
		}
		if u.PaymentDefaults >= s.auction.MaxPaymentDefaults {
			continue
		}
		fee, payout, err := money.SplitFee(c.AmountPaise, s.auction.FeeBps)
		if err != nil {
			return nil, err
		}
		due := s.clock.Now().Add(s.auction.PaymentDeadline)
		method := string(MethodPlatform)
		bidID := c.BidID
		row, err := q.InsertOrder(ctx, store.InsertOrderParams{
			ListingID:         l.ID,
			BuyerID:           c.BidderID,
			SellerID:          l.SellerID,
			Kind:              string(KindAuction),
			Status:            string(StatusAwaitingPayment),
			PricePaise:        c.AmountPaise,
			PlatformFeeBps:    int32(s.auction.FeeBps),
			PlatformFeePaise:  fee,
			SellerPayoutPaise: payout,
			PaymentMethod:     &method,
			AuctionRound:      &round,
			WinningBidID:      &bidID,
			PaymentDueAt:      &due,
		})
		if err != nil {
			return nil, err
		}
		if _, err := s.listings.SetStatus(ctx, tx, l.ID, listings.StatusReserved); err != nil {
			return nil, err
		}
		o := fromRow(row)
		return &o, nil
	}
	return nil, nil
}

// AfterAward runs once the awarding transaction has committed: it schedules the
// payment deadline and tells the winner and seller.
func (s *Service) AfterAward(ctx context.Context, o *Order) {
	if o == nil {
		return
	}
	s.schedulePaymentExpiry(ctx, o.ID, *o.PaymentDueAt)
	if s.notify != nil {
		s.notify.PublishToUsers(ctx, []uuid.UUID{o.BuyerID}, "auction.won", map[string]any{
			"order_id": o.ID, "listing_id": o.ListingID, "amount_paise": o.PricePaise, "payment_due_at": o.PaymentDueAt,
		})
	}
	s.publish(ctx, *o, "order.updated")
}

// ExpireAuctionPayment expires an unpaid auction order at its deadline, flags the
// buyer, and offers the item to the next bidder (or puts it back on sale). Safe to run
// repeatedly: anything not overdue and unpaid is left alone.
func (s *Service) ExpireAuctionPayment(ctx context.Context, orderID uuid.UUID) error {
	var (
		expired   Order
		next      *Order
		hostel    string
		holdUntil *time.Time
		acted     bool
	)
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		o, l, err := s.lockOrder(ctx, tx, orderID)
		if err != nil {
			return err
		}
		if o.Kind != KindAuction || o.Status != StatusAwaitingPayment || o.PaymentDueAt == nil ||
			s.clock.Now().Before(*o.PaymentDueAt) {
			return nil
		}
		if s.payments != nil {
			if holdUntil, err = s.payments.HoldExpiry(ctx, tx, o); err != nil || holdUntil != nil {
				return err
			}
			if err := s.payments.ExpirePayments(ctx, tx, o.ID); err != nil {
				return err
			}
		}

		reason := "payment deadline passed"
		row, err := store.New(tx).TransitionOrder(ctx, store.TransitionOrderParams{
			ID: o.ID, FromStatus: string(StatusAwaitingPayment), ToStatus: string(StatusExpired), CloseReason: &reason,
		})
		if err != nil {
			return err
		}
		expired, acted, hostel = fromRow(row), true, l.Hostel

		// Flag the buyer; enough defaults block them from bidding.
		if _, err := s.users.IncrementPaymentDefaults(ctx, tx, o.BuyerID); err != nil {
			return err
		}
		if next, err = s.AwardAuction(ctx, tx, l); err != nil {
			return err
		}
		if next == nil {
			// Nobody left: the round stays closed and the item is back on sale; the
			// seller can start a new round or switch to a fixed price.
			_, err = s.listings.SetStatus(ctx, tx, l.ID, listings.StatusActive)
		}
		return err
	})
	if err != nil {
		return err
	}
	if holdUntil != nil {
		s.schedulePaymentExpiry(ctx, orderID, *holdUntil)
		return nil
	}
	if !acted {
		return nil
	}
	s.publish(ctx, expired, "order.updated")
	s.AfterAward(ctx, next)
	s.listings.FeedChanged(ctx, hostel)
	return nil
}

// MarkAuctionPaid moves an auction order to paid inside the payments module's
// transaction (after a verified webhook or an admin action) and marks the item sold.
func (s *Service) MarkAuctionPaid(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (Order, error) {
	o, l, err := s.lockOrder(ctx, tx, orderID)
	if err != nil {
		return Order{}, err
	}
	if o.Kind != KindAuction {
		return Order{}, httpx.Conflict("not_auction_order", "only auction orders are paid to the platform")
	}
	if o.Status != StatusAwaitingPayment {
		return Order{}, httpx.Conflict("invalid_transition", fmt.Sprintf("can't mark an order that is %s as paid", o.Status))
	}
	row, err := store.New(tx).TransitionOrder(ctx, store.TransitionOrderParams{
		ID: o.ID, FromStatus: string(StatusAwaitingPayment), ToStatus: string(StatusPaid),
	})
	if err != nil {
		return Order{}, err
	}
	if _, err := s.markSold(ctx, tx, o, l); err != nil {
		return Order{}, err
	}
	return fromRow(row), nil
}

// OrderPaid runs after the payment transaction commits.
func (s *Service) OrderPaid(ctx context.Context, o Order, hostel string) {
	s.listings.FeedChanged(ctx, hostel)
	s.publish(ctx, o, "order.updated")
}

// LockForPayment returns the order and its listing, locked in that order.
func (s *Service) LockForPayment(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (Order, listings.Listing, error) {
	return s.lockOrder(ctx, tx, orderID)
}

// lockOrder locks the listing row, then the order row (the global lock order).
func (s *Service) lockOrder(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (Order, listings.Listing, error) {
	q := store.New(tx)
	peek, err := q.GetOrder(ctx, orderID)
	if db.IsNoRows(err) {
		return Order{}, listings.Listing{}, errNotFound
	}
	if err != nil {
		return Order{}, listings.Listing{}, err
	}
	l, err := s.listings.Lock(ctx, tx, peek.ListingID)
	if err != nil {
		return Order{}, listings.Listing{}, err
	}
	row, err := q.GetOrderForUpdate(ctx, orderID)
	if err != nil {
		return Order{}, listings.Listing{}, err
	}
	return fromRow(row), l, nil
}

func (s *Service) schedulePaymentExpiry(ctx context.Context, orderID uuid.UUID, at time.Time) {
	b, _ := json.Marshal(orderPayload{OrderID: orderID})
	id := "expire-payment:" + orderID.String() + ":" + strconv.FormatInt(at.Unix(), 10)
	err := s.jobs.Enqueue(ctx, asynq.NewTask(TaskExpirePayment, b), asynq.ProcessAt(at), asynq.TaskID(id), asynq.MaxRetry(10))
	if err != nil {
		slog.ErrorContext(ctx, "schedule payment expiry failed", "order_id", orderID, "err", err)
	}
}

func (s *Service) HandleExpirePayment(ctx context.Context, t *asynq.Task) error {
	var p orderPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
	}
	return s.ExpireAuctionPayment(ctx, p.OrderID)
}
