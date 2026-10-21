package bidding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"hostelmarket/internal/listings"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/ratelimit"
	"hostelmarket/internal/platform/reqctx"
	"hostelmarket/internal/platform/ws"
)

type Handler struct {
	svc   *Service
	limit *ratelimit.Policy
}

func NewHandler(svc *Service, bidLimit *ratelimit.Policy) *Handler {
	return &Handler{svc: svc, limit: bidLimit}
}

// Routes registers bidding endpoints; the caller must require authentication.
func (h *Handler) Routes(r chi.Router) {
	r.With(h.limit.Middleware(ratelimit.ByUser)).Post("/listings/{id}/bids", httpx.Handle(h.place))
	r.Get("/listings/{id}/bids", httpx.Handle(h.history))
	r.Get("/listings/{id}/auction", httpx.Handle(h.auction))
}

// RegisterWS adds live bid subscriptions to the gateway:
//
//	{"type":"listing.subscribe","id":"s1","data":{"listing_id":"..."}}
//
// The ack carries the current auction state, so a reconnecting client is in sync the
// moment it re-subscribes; bid.placed / auction.closed events follow.
func (h *Handler) RegisterWS(g *ws.Gateway) {
	g.Handle("listing.subscribe", func(ctx context.Context, c *ws.Conn, data json.RawMessage) (any, error) {
		id, err := listingIDFrom(data)
		if err != nil {
			return nil, err
		}
		view, err := h.liveView(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := c.Subscribe(ctx, ws.ListingChannel(id)); err != nil {
			return nil, err
		}
		return view, nil
	})
	g.Handle("listing.unsubscribe", func(_ context.Context, c *ws.Conn, data json.RawMessage) (any, error) {
		id, err := listingIDFrom(data)
		if err != nil {
			return nil, err
		}
		c.Unsubscribe(ws.ListingChannel(id))
		return map[string]any{"listing_id": id}, nil
	})
}

type placeBody struct {
	AmountPaise int64 `json:"amount_paise" validate:"required,gt=0"`
}

func (h *Handler) place(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("listing not found")
	}
	var body placeBody
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		return err
	}
	uid, _ := reqctx.UserID(r.Context())
	bid, err := h.svc.PlaceBid(r.Context(), uid, id, body.AmountPaise)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, bid)
	return nil
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("listing not found")
	}
	qv := r.URL.Query()
	limit := int32(50)
	if s := qv.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			return queryErr("limit", "must be between 1 and 100")
		}
		limit = int32(n)
	}
	var round *int32
	if s := qv.Get("round"); s != "" {
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil || n < 1 {
			return queryErr("round", "must be a positive integer")
		}
		r32 := int32(n)
		round = &r32
	}
	var before *int64
	if s := qv.Get("before"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 1 {
			return queryErr("before", "must be a bid id")
		}
		before = &n
	}
	page, err := h.svc.History(r.Context(), id, round, before, limit)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, page)
	return nil
}

// AuctionView is the live state of an auction (highest bid read from Redis).
type AuctionView struct {
	ListingID          uuid.UUID  `json:"listing_id"`
	Round              int32      `json:"round"`
	Open               bool       `json:"open"`
	EndsAt             time.Time  `json:"ends_at"`
	StartingPricePaise int64      `json:"starting_price_paise"`
	MinIncrementPaise  int64      `json:"min_increment_paise"`
	HighestBidPaise    *int64     `json:"highest_bid_paise"`
	HighestBidderID    *uuid.UUID `json:"highest_bidder_id"`
	MinNextBidPaise    int64      `json:"min_next_bid_paise"`
	ServerTime         time.Time  `json:"server_time"`
}

func (h *Handler) auction(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("listing not found")
	}
	view, err := h.liveView(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, view)
	return nil
}

func (h *Handler) liveView(ctx context.Context, id uuid.UUID) (AuctionView, error) {
	l, err := h.svc.listings.GetStrong(ctx, id)
	if errors.Is(err, listings.ErrNotFound) || (err == nil && l.Status == listings.StatusRemoved) {
		return AuctionView{}, httpx.NotFound("listing not found")
	}
	if err != nil {
		return AuctionView{}, httpx.Internal(err)
	}
	if l.Auction == nil {
		return AuctionView{}, errNotAuction
	}
	live, err := h.svc.LiveAuction(ctx, l)
	if err != nil {
		return AuctionView{}, httpx.Unavailable("bidding_unavailable", "live auction state is unavailable")
	}
	return AuctionView{
		ListingID: l.ID, Round: l.Auction.Round, Open: live.Open, EndsAt: live.EndsAt,
		StartingPricePaise: l.Auction.StartingPricePaise, MinIncrementPaise: l.Auction.MinIncrementPaise,
		HighestBidPaise: live.HighestBidPaise, HighestBidderID: live.HighestBidderID,
		MinNextBidPaise: live.MinNextBidPaise, ServerTime: h.svc.clock.Now().UTC(),
	}, nil
}

func listingIDFrom(data json.RawMessage) (uuid.UUID, error) {
	var in struct {
		ListingID uuid.UUID `json:"listing_id"`
	}
	if err := json.Unmarshal(data, &in); err != nil || in.ListingID == uuid.Nil {
		return uuid.Nil, httpx.BadRequest("invalid_json", "data must be {listing_id}")
	}
	return in.ListingID, nil
}

func queryErr(field, msg string) error {
	return httpx.ValidationFailed(httpx.FieldError{Field: field, Rule: "query", Message: msg})
}
