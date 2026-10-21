// Package bidding runs auctions. Bids are validated and applied atomically in Redis
// (Lua), then persisted in Postgres, which stays the source of truth: the Postgres
// write re-checks the amount under the listing's row lock, and Redis is reverted or
// re-synced whenever the two disagree.
package bidding

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
	"github.com/redis/go-redis/v9"

	"hostelmarket/internal/bidding/store"
	"hostelmarket/internal/listings"
	"hostelmarket/internal/orders"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/jobs"
	"hostelmarket/internal/platform/money"
	"hostelmarket/internal/platform/ws"
	"hostelmarket/internal/users"
)

const (
	TaskCloseAuction = "bidding:close_auction"
	TaskSweep        = "bidding:sweep"

	// closeTolerance absorbs clock skew between Redis TIME and this server when the
	// close job double-checks the end time stored in Postgres.
	closeTolerance = 2 * time.Second
)

type ListingStore interface {
	GetStrong(ctx context.Context, id uuid.UUID) (listings.Listing, error)
	Lock(ctx context.Context, tx db.DBTX, id uuid.UUID) (listings.Listing, error)
	ApplyBid(ctx context.Context, tx db.DBTX, id uuid.UUID, amount int64, endsAt time.Time) (listings.Listing, error)
	CloseAuctionRound(ctx context.Context, tx db.DBTX, id uuid.UUID) (listings.Listing, bool, error)
	OpenAuctions(ctx context.Context, limit int32) ([]listings.Listing, error)
	DueAuctions(ctx context.Context) ([]listings.DueAuction, error)
	FeedChanged(ctx context.Context, hostel string)
}

type UserStore interface {
	Get(ctx context.Context, id uuid.UUID) (users.User, error)
}

// Awarder turns a closed round into an order (package orders).
type Awarder interface {
	AwardAuction(ctx context.Context, tx pgx.Tx, l listings.Listing) (*orders.Order, error)
	AfterAward(ctx context.Context, o *orders.Order)
}

type Publisher interface {
	Publish(ctx context.Context, channel string, ev ws.Event) error
	PublishToUsers(ctx context.Context, userIDs []uuid.UUID, eventType string, data any)
}

type Config struct {
	AntiSnipeWindow    time.Duration
	AntiSnipeExtension time.Duration
	MaxPaymentDefaults int32
}

type Service struct {
	db       *db.Router
	listings ListingStore
	users    UserStore
	orders   Awarder
	state    *state
	pub      Publisher
	jobs     jobs.Enqueuer
	clock    clock.Clock
	cfg      Config
}

func NewService(router *db.Router, rdb redis.UniversalClient, l ListingStore, u UserStore, o Awarder,
	pub Publisher, enq jobs.Enqueuer, clk clock.Clock, cfg Config) *Service {
	return &Service{db: router, listings: l, users: u, orders: o, state: &state{rdb: rdb},
		pub: pub, jobs: enq, clock: clk, cfg: cfg}
}

type Bid struct {
	ID          int64     `json:"id"`
	ListingID   uuid.UUID `json:"listing_id"`
	Round       int32     `json:"round"`
	BidderID    uuid.UUID `json:"bidder_id"`
	AmountPaise int64     `json:"amount_paise"`
	CreatedAt   time.Time `json:"created_at"`
}

// PlacedBid is a bid plus the auction state right after it.
type PlacedBid struct {
	Bid
	EndsAt          time.Time `json:"ends_at"`
	Extended        bool      `json:"extended"`
	MinNextBidPaise int64     `json:"min_next_bid_paise"`
}

var (
	errNotAuction    = httpx.Conflict("not_an_auction", "this listing is sold at a fixed price; send a purchase request instead")
	errAuctionClosed = httpx.Conflict("auction_closed", "bidding has ended for this listing")
	errOutbid        = httpx.Conflict("outbid", "a higher bid was placed first; refresh and bid again")
)

// PlaceBid accepts a bid only if bidding is enabled, the auction is open, the bidder
// passes every eligibility rule, isn't the seller, and amount >= highest + increment.
func (s *Service) PlaceBid(ctx context.Context, bidderID, listingID uuid.UUID, amount int64) (PlacedBid, error) {
	if amount <= 0 || amount > money.MaxPaise {
		return PlacedBid{}, httpx.ValidationFailed(httpx.FieldError{Field: "amount_paise", Rule: "range", Message: "must be between 1 and 1000000000 paise"})
	}
	bidder, err := s.users.Get(ctx, bidderID)
	if err != nil {
		return PlacedBid{}, httpx.Internal(err)
	}
	l, err := s.listings.GetStrong(ctx, listingID)
	if errors.Is(err, listings.ErrNotFound) {
		return PlacedBid{}, httpx.NotFound("listing not found")
	}
	if err != nil {
		return PlacedBid{}, httpx.Internal(err)
	}
	if !l.BiddingEnabled || l.Auction == nil {
		return PlacedBid{}, errNotAuction
	}
	if err := l.CheckBuyer(listings.BuyerFrom(bidder), s.clock.Now()); err != nil {
		return PlacedBid{}, err
	}
	if l.Auction.ClosedAt != nil {
		return PlacedBid{}, errAuctionClosed
	}
	if bidder.PaymentDefaults >= s.cfg.MaxPaymentDefaults {
		return PlacedBid{}, httpx.Forbidden("bidding_blocked", "you have let too many auction wins go unpaid; contact an admin")
	}

	// 1. Redis: atomic validate-and-apply.
	token := uuid.NewString()
	res, err := s.placeInRedis(ctx, l, bidderID, amount, token)
	if err != nil {
		return PlacedBid{}, err
	}

	// 2. Postgres: persist under the listing lock, re-checking against stored state.
	var bid store.Bid
	err = s.db.WithTx(ctx, func(tx pgx.Tx) error {
		lt, err := s.listings.Lock(ctx, tx, listingID)
		if err != nil {
			return err
		}
		a := lt.Auction
		if !lt.BiddingEnabled || a == nil || lt.Status != listings.StatusActive || a.ClosedAt != nil || a.Round != l.Auction.Round {
			return errAuctionClosed
		}
		if amount < a.StartingPricePaise || (a.CurrentBidPaise != nil && amount < *a.CurrentBidPaise+a.MinIncrementPaise) {
			return errOutbid
		}
		bid, err = store.New(tx).InsertBid(ctx, store.InsertBidParams{ListingID: listingID, AuctionRound: a.Round, BidderID: bidderID, AmountPaise: amount})
		if err != nil {
			return err
		}
		_, err = s.listings.ApplyBid(ctx, tx, listingID, amount, res.NewEnds)
		return err
	})
	if err != nil {
		if errors.Is(err, errAuctionClosed) {
			s.resync(ctx, listingID) // Redis thought it was open; Postgres wins
			return PlacedBid{}, errAuctionClosed
		}
		if errors.Is(err, errOutbid) || db.IsUniqueViolation(err) {
			// A higher bid reached Postgres first. If Redis still shows this bid on top,
			// Redis is behind Postgres: rebuild it. Otherwise Redis already moved on.
			if head, herr := s.state.headToken(ctx, listingID); herr != nil || head == token {
				s.resync(ctx, listingID)
			}
			return PlacedBid{}, errOutbid
		}
		if rerr := s.state.revert(context.WithoutCancel(ctx), listingID, token, res.Prev); rerr != nil {
			slog.ErrorContext(ctx, "revert bid in redis failed", "listing_id", listingID, "err", rerr)
		}
		return PlacedBid{}, httpx.Internal(fmt.Errorf("persist bid: %w", err))
	}

	placed := PlacedBid{
		Bid:             toBid(bid),
		EndsAt:          res.NewEnds,
		Extended:        res.Extended,
		MinNextBidPaise: amount + l.Auction.MinIncrementPaise,
	}
	if res.Extended {
		s.scheduleClose(ctx, listingID, bid.AuctionRound, res.NewEnds)
	}
	s.broadcastBid(ctx, placed, res.Prev.Bidder)
	return placed, nil
}

func (s *Service) placeInRedis(ctx context.Context, l listings.Listing, bidderID uuid.UUID, amount int64, token string) (placeResult, error) {
	for attempt := 0; ; attempt++ {
		res, err := s.state.place(ctx, l.ID, bidderID, amount, token, l.Auction.Round, s.cfg.AntiSnipeWindow, s.cfg.AntiSnipeExtension)
		if err != nil {
			return placeResult{}, httpx.Unavailable("bidding_unavailable", "bidding is temporarily unavailable, try again")
		}
		switch res.Status {
		case "ok":
			return res, nil
		case "missing", "round_mismatch":
			if attempt > 0 {
				return placeResult{}, httpx.Unavailable("bidding_unavailable", "auction state is being rebuilt, try again")
			}
			if err := s.syncFromDB(ctx, l); err != nil {
				return placeResult{}, httpx.Internal(err)
			}
		case "closed", "ended":
			return placeResult{}, errAuctionClosed
		case "own_listing":
			return placeResult{}, httpx.Forbidden("own_listing", "you cannot bid on your own listing")
		case "already_highest":
			return placeResult{}, httpx.Conflict("already_highest", "you already have the highest bid")
		case "too_low":
			return placeResult{}, httpx.Conflict("bid_too_low", "bid must be at least "+money.Rupees(res.MinRequired)+" rupees").
				WithDetails(map[string]int64{"min_bid_paise": res.MinRequired})
		default:
			return placeResult{}, httpx.Internal(fmt.Errorf("unexpected place status %q", res.Status))
		}
	}
}

// CloseAuction ends a round and awards it. It is idempotent and safe to run any number
// of times, concurrently: a closed round is recognised under the row lock and skipped,
// and the one-live-order index backs that up.
func (s *Service) CloseAuction(ctx context.Context, listingID uuid.UUID, round int32) error {
	l, err := s.listings.GetStrong(ctx, listingID)
	if errors.Is(err, listings.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !l.BiddingEnabled || l.Auction == nil || l.Auction.Round != round || l.Auction.ClosedAt != nil || l.Status != listings.StatusActive {
		return nil // already closed, removed or re-configured
	}

	status, redisEnds, err := s.state.close(ctx, listingID, round)
	if err != nil {
		return err
	}
	switch status {
	case "not_due": // extended by a late bid: try again at the new end
		s.scheduleClose(ctx, listingID, round, redisEnds)
		return nil
	case "missing", "round_mismatch": // Redis lost it; decide from Postgres alone
		if s.clock.Now().Before(l.Auction.EndsAt) {
			if err := s.syncFromDB(ctx, l); err != nil {
				return err
			}
			s.scheduleClose(ctx, listingID, round, l.Auction.EndsAt)
			return nil
		}
	}

	var (
		award   *orders.Order
		closed  listings.Listing
		did     bool
		pgEnds  time.Time
		tooSoon bool
	)
	err = s.db.WithTx(ctx, func(tx pgx.Tx) error {
		lt, err := s.listings.Lock(ctx, tx, listingID)
		if err != nil {
			return err
		}
		a := lt.Auction
		if !lt.BiddingEnabled || a == nil || a.Round != round || a.ClosedAt != nil || lt.Status != listings.StatusActive {
			return nil
		}
		if a.EndsAt.Sub(s.clock.Now()) > closeTolerance {
			tooSoon, pgEnds = true, a.EndsAt // a persisted extension Redis didn't know about
			return nil
		}
		if closed, did, err = s.listings.CloseAuctionRound(ctx, tx, listingID); err != nil || !did {
			return err
		}
		award, err = s.orders.AwardAuction(ctx, tx, closed)
		return err
	})
	if err != nil {
		return fmt.Errorf("close auction %s: %w", listingID, err)
	}
	if tooSoon {
		s.resync(ctx, listingID)
		s.scheduleClose(ctx, listingID, round, pgEnds)
		return nil
	}
	if !did {
		return nil
	}

	s.resync(ctx, listingID)
	s.orders.AfterAward(ctx, award)
	s.listings.FeedChanged(ctx, closed.Hostel)
	data := map[string]any{"listing_id": listingID, "round": round, "sold": award != nil}
	if award != nil {
		data["winner_id"], data["amount_paise"] = award.BuyerID, award.PricePaise
	}
	s.publishListing(ctx, listingID, "auction.closed", data)
	return nil
}

// RankedBidders implements orders.Bidders.
func (s *Service) RankedBidders(ctx context.Context, q db.DBTX, listingID uuid.UUID, round int32) ([]orders.Candidate, error) {
	rows, err := store.New(q).RankedBidders(ctx, store.RankedBiddersParams{ListingID: listingID, AuctionRound: round})
	if err != nil {
		return nil, err
	}
	out := make([]orders.Candidate, len(rows))
	for i, r := range rows {
		out[i] = orders.Candidate{BidID: r.ID, BidderID: r.BidderID, AmountPaise: r.AmountPaise}
	}
	return out, nil
}

// AuctionChanged implements listings.AuctionHooks: (re)load Redis state and schedule
// the close, or drop the state when the listing is no longer a running auction.
func (s *Service) AuctionChanged(ctx context.Context, l listings.Listing) error {
	if !l.BiddingEnabled || l.Auction == nil || l.Status != listings.StatusActive {
		if err := s.state.remove(ctx, l.ID); err != nil {
			return err
		}
		s.publishListing(ctx, l.ID, "auction.closed", map[string]any{"listing_id": l.ID, "sold": false, "cancelled": true})
		return nil
	}
	if err := s.syncFromDB(ctx, l); err != nil {
		return err
	}
	if l.Auction.ClosedAt == nil {
		s.scheduleClose(ctx, l.ID, l.Auction.Round, l.Auction.EndsAt)
	}
	return nil
}

// LiveAuction implements listings.LiveAuctionReader. The highest bid always comes
// from Redis (rebuilt from the primary if missing), never from the replica.
func (s *Service) LiveAuction(ctx context.Context, l listings.Listing) (listings.LiveAuction, error) {
	if l.Auction == nil {
		return listings.LiveAuction{}, errNotAuction
	}
	snap, ok, err := s.state.live(ctx, l.ID)
	if err != nil {
		return listings.LiveAuction{}, err
	}
	if !ok || snap.Round != l.Auction.Round {
		fresh, err := s.listings.GetStrong(ctx, l.ID)
		if err != nil {
			return listings.LiveAuction{}, err
		}
		if err := s.syncFromDB(ctx, fresh); err != nil {
			return listings.LiveAuction{}, err
		}
		if snap, _, err = s.state.live(ctx, l.ID); err != nil {
			return listings.LiveAuction{}, err
		}
	}
	live := listings.LiveAuction{
		Open:            !snap.Closed && s.clock.Now().Before(snap.Ends) && l.Status == listings.StatusActive,
		EndsAt:          snap.Ends,
		MinNextBidPaise: snap.minNext(),
	}
	if snap.High > 0 {
		h, b := snap.High, snap.Bidder
		live.HighestBidPaise, live.HighestBidderID = &h, &b
	}
	return live, nil
}

// RebuildState reloads every running auction into Redis from Postgres and (re)schedules
// its close job. Run at startup; also safe to run any time.
func (s *Service) RebuildState(ctx context.Context) error {
	open, err := s.listings.OpenAuctions(ctx, 5000)
	if err != nil {
		return err
	}
	var failed int
	for _, l := range open {
		if err := s.syncFromDB(ctx, l); err != nil {
			failed++
			slog.ErrorContext(ctx, "rebuild auction state failed", "listing_id", l.ID, "err", err)
			continue
		}
		s.scheduleClose(ctx, l.ID, l.Auction.Round, l.Auction.EndsAt)
	}
	slog.InfoContext(ctx, "auction state rebuilt from postgres", "auctions", len(open), "failed", failed)
	return nil
}

type BidPage struct {
	Items      []Bid  `json:"items"`
	NextBefore *int64 `json:"next_before,omitempty"`
}

// History lists a round's bids, newest first, from the primary.
func (s *Service) History(ctx context.Context, listingID uuid.UUID, round *int32, before *int64, limit int32) (BidPage, error) {
	if round == nil {
		l, err := s.listings.GetStrong(ctx, listingID)
		if errors.Is(err, listings.ErrNotFound) {
			return BidPage{}, httpx.NotFound("listing not found")
		}
		if err != nil {
			return BidPage{}, httpx.Internal(err)
		}
		if l.Auction == nil {
			return BidPage{Items: []Bid{}}, nil
		}
		round = &l.Auction.Round
	}
	rows, err := store.New(s.db.Reader(ctx, db.Strong)).ListBids(ctx, store.ListBidsParams{
		ListingID: listingID, AuctionRound: *round, BeforeID: before, RowLimit: limit + 1,
	})
	if err != nil {
		return BidPage{}, httpx.Internal(err)
	}
	page := BidPage{Items: make([]Bid, 0, len(rows))}
	for i, r := range rows {
		if i == int(limit) {
			last := page.Items[len(page.Items)-1].ID
			page.NextBefore = &last
			break
		}
		page.Items = append(page.Items, toBid(r))
	}
	return page, nil
}

// --- internals ---------------------------------------------------------------------

func (s *Service) syncFromDB(ctx context.Context, l listings.Listing) error {
	if l.Auction == nil {
		return s.state.remove(ctx, l.ID)
	}
	a := l.Auction
	snap := snapshot{
		Round: a.Round, Seller: l.SellerID, Start: a.StartingPricePaise, Incr: a.MinIncrementPaise,
		Ends: a.EndsAt, Closed: a.ClosedAt != nil || l.Status != listings.StatusActive,
	}
	top, err := store.New(s.db.Reader(ctx, db.Strong)).TopBid(ctx, store.TopBidParams{ListingID: l.ID, AuctionRound: a.Round})
	switch {
	case err == nil:
		snap.High, snap.Bidder, snap.Token = top.AmountPaise, top.BidderID, "db:"+strconv.FormatInt(top.ID, 10)
	case !db.IsNoRows(err):
		return fmt.Errorf("top bid: %w", err)
	}
	return s.state.load(ctx, l.ID, snap)
}

// resync reloads one listing's Redis state from the primary, logging failures.
func (s *Service) resync(ctx context.Context, listingID uuid.UUID) {
	ctx = context.WithoutCancel(ctx)
	l, err := s.listings.GetStrong(ctx, listingID)
	if err == nil {
		err = s.syncFromDB(ctx, l)
	}
	if err != nil {
		slog.ErrorContext(ctx, "resync auction state failed", "listing_id", listingID, "err", err)
	}
}

type closePayload struct {
	ListingID uuid.UUID `json:"listing_id"`
	Round     int32     `json:"round"`
}

// scheduleClose enqueues the close job for the end time. Each (round, end time) gets
// its own task ID, so an extension adds a new task and the old one finds the auction
// not yet due and reschedules (or no-ops).
func (s *Service) scheduleClose(ctx context.Context, listingID uuid.UUID, round int32, at time.Time) {
	b, _ := json.Marshal(closePayload{ListingID: listingID, Round: round})
	id := fmt.Sprintf("auction-close:%s:%d:%d", listingID, round, at.UnixMilli())
	err := s.jobs.Enqueue(context.WithoutCancel(ctx), asynq.NewTask(TaskCloseAuction, b),
		asynq.ProcessAt(at), asynq.TaskID(id), asynq.MaxRetry(20))
	if err != nil {
		slog.ErrorContext(ctx, "schedule auction close failed (sweep will catch it)", "listing_id", listingID, "err", err)
	}
}

func (s *Service) HandleCloseTask(ctx context.Context, t *asynq.Task) error {
	var p closePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
	}
	return s.CloseAuction(ctx, p.ListingID, p.Round)
}

// HandleSweep closes any auction whose end passed without its task running.
func (s *Service) HandleSweep(ctx context.Context, _ *asynq.Task) error {
	due, err := s.listings.DueAuctions(ctx)
	if err != nil {
		return err
	}
	for _, d := range due {
		if err := s.CloseAuction(ctx, d.ID, d.Round); err != nil {
			slog.ErrorContext(ctx, "sweep: close auction failed", "listing_id", d.ID, "err", err)
		}
	}
	return nil
}

func (s *Service) RegisterJobs(w jobs.Registry) error {
	w.Handle(TaskCloseAuction, s.HandleCloseTask)
	w.Handle(TaskSweep, s.HandleSweep)
	return w.Every("@every 1m", asynq.NewTask(TaskSweep, nil), asynq.Unique(50*time.Second))
}

func (s *Service) broadcastBid(ctx context.Context, b PlacedBid, prevBidder string) {
	s.publishListing(ctx, b.ListingID, "bid.placed", b)
	if prev, err := uuid.Parse(prevBidder); err == nil && prev != b.BidderID {
		s.pub.PublishToUsers(ctx, []uuid.UUID{prev}, "bid.outbid", map[string]any{
			"listing_id": b.ListingID, "amount_paise": b.AmountPaise, "min_next_bid_paise": b.MinNextBidPaise,
		})
	}
}

func (s *Service) publishListing(ctx context.Context, listingID uuid.UUID, typ string, data any) {
	if err := s.pub.Publish(context.WithoutCancel(ctx), ws.ListingChannel(listingID), ws.Event{Type: typ, Data: data}); err != nil {
		slog.WarnContext(ctx, "publish listing event failed", "type", typ, "err", err)
	}
}

func toBid(r store.Bid) Bid {
	return Bid{ID: r.ID, ListingID: r.ListingID, Round: r.AuctionRound, BidderID: r.BidderID, AmountPaise: r.AmountPaise, CreatedAt: r.CreatedAt}
}
