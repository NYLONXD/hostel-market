package listings

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/reqctx"
	"hostelmarket/internal/users"
)

// LiveAuctionReader supplies live auction state for the detail view (bidding module).
type LiveAuctionReader interface {
	LiveAuction(ctx context.Context, l Listing) (LiveAuction, error)
}

type Handler struct {
	svc   *Service
	users UserDirectory
	live  LiveAuctionReader // nil until bidding is wired
	clock clock.Clock
}

func NewHandler(svc *Service, u UserDirectory, live LiveAuctionReader, clk clock.Clock) *Handler {
	return &Handler{svc: svc, users: u, live: live, clock: clk}
}

// Routes registers listing endpoints; the caller must require authentication.
func (h *Handler) Routes(r chi.Router) {
	r.Get("/listings", httpx.Handle(h.browse))
	r.Post("/listings", httpx.Handle(h.create))
	r.Get("/listings/{id}", httpx.Handle(h.detail))
	r.Patch("/listings/{id}", httpx.Handle(h.update))
	r.Delete("/listings/{id}", httpx.Handle(h.remove))
	r.Get("/users/me/listings", httpx.Handle(h.mine))
}

// --- views --------------------------------------------------------------------

type ListingView struct {
	ID                uuid.UUID         `json:"id"`
	SellerID          uuid.UUID         `json:"seller_id"`
	Seller            *users.PublicView `json:"seller,omitempty"`
	Hostel            string            `json:"hostel"`
	Title             string            `json:"title"`
	Description       string            `json:"description"`
	Category          string            `json:"category"`
	Condition         string            `json:"condition"`
	Status            Status            `json:"status"`
	BiddingEnabled    bool              `json:"bidding_enabled"`
	PricePaise        *int64            `json:"price_paise,omitempty"`
	DisplayPricePaise int64             `json:"display_price_paise"`
	Auction           *AuctionView      `json:"auction,omitempty"`
	Eligibility       RulesView         `json:"eligibility"`
	ViewerEligibility *EligibilityView  `json:"viewer_eligibility,omitempty"`
	Photos            []PhotoView       `json:"photos"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

type AuctionView struct {
	StartingPricePaise int64      `json:"starting_price_paise"`
	MinIncrementPaise  int64      `json:"min_increment_paise"`
	EndsAt             time.Time  `json:"ends_at"`
	Round              int32      `json:"round"`
	Open               bool       `json:"open"`
	HighestBidPaise    *int64     `json:"highest_bid_paise"`
	HighestBidderID    *uuid.UUID `json:"highest_bidder_id,omitempty"`
	BidCount           int32      `json:"bid_count"`
	MinNextBidPaise    int64      `json:"min_next_bid_paise"`
	SettingsLocked     bool       `json:"settings_locked"`
}

type RulesView struct {
	AvailableFrom  *string `json:"available_from"`
	AvailableUntil *string `json:"available_until"`
	Timezone       string  `json:"timezone"`
	AllowedBlock   *string `json:"allowed_block"`
	AllowedYear    *int16  `json:"allowed_year"`
}

// EligibilityView is advisory: the server re-checks on every bid and purchase.
type EligibilityView struct {
	Eligible bool   `json:"eligible"`
	Code     string `json:"code,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type PhotoView struct {
	PublicID string `json:"public_id"`
	URL      string `json:"url"`
	Width    int32  `json:"width,omitempty"`
	Height   int32  `json:"height,omitempty"`
}

func NewListingView(l Listing, now time.Time) ListingView {
	v := ListingView{
		ID: l.ID, SellerID: l.SellerID, Hostel: l.Hostel, Title: l.Title, Description: l.Description,
		Category: l.Category, Condition: l.Condition, Status: l.Status, BiddingEnabled: l.BiddingEnabled,
		PricePaise: l.PricePaise, DisplayPricePaise: l.DisplayPricePaise,
		Eligibility: RulesView{Timezone: "Asia/Kolkata", AllowedBlock: l.Rules.AllowedBlock, AllowedYear: l.Rules.AllowedYear},
		Photos:      make([]PhotoView, len(l.Photos)),
		CreatedAt:   l.CreatedAt, UpdatedAt: l.UpdatedAt,
	}
	if w := l.Rules.Window; w != nil {
		from, until := w.From.String(), w.Until.String()
		v.Eligibility.AvailableFrom, v.Eligibility.AvailableUntil = &from, &until
	}
	if a := l.Auction; a != nil {
		v.Auction = &AuctionView{
			StartingPricePaise: a.StartingPricePaise, MinIncrementPaise: a.MinIncrementPaise, EndsAt: a.EndsAt,
			Round: a.Round, Open: a.ClosedAt == nil && now.Before(a.EndsAt) && l.Status == StatusActive,
			HighestBidPaise: a.CurrentBidPaise, BidCount: a.BidCount, MinNextBidPaise: a.MinNextBid(),
			SettingsLocked: a.SettingsLocked(),
		}
	}
	for i, p := range l.Photos {
		v.Photos[i] = PhotoView{PublicID: p.PublicID, URL: p.URL, Width: p.Width, Height: p.Height}
	}
	return v
}

// --- handlers -----------------------------------------------------------------

func (h *Handler) browse(w http.ResponseWriter, r *http.Request) error {
	viewer, err := h.viewer(r.Context())
	if err != nil {
		return err
	}
	f, err := parseFilter(r.URL.Query())
	if err != nil {
		return err
	}
	f.Hostel = viewer.Hostel
	page, err := h.svc.Browse(r.Context(), f)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, page)
	return nil
}

func (h *Handler) mine(w http.ResponseWriter, r *http.Request) error {
	qv := r.URL.Query()
	f := MineFilter{Cursor: qv.Get("cursor")}
	limit, err := parseLimit(qv.Get("limit"))
	if err != nil {
		return err
	}
	f.Limit = limit
	if s := qv.Get("status"); s != "" {
		st := Status(s)
		if !slices.Contains([]Status{StatusActive, StatusReserved, StatusSold, StatusRemoved}, st) {
			return queryErr("status", "must be one of: active reserved sold removed")
		}
		f.Status = &st
	}
	uid, _ := reqctx.UserID(r.Context())
	page, err := h.svc.ListMine(r.Context(), uid, f)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, page)
	return nil
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return errNotFound
	}
	ctx := r.Context()
	uid, _ := reqctx.UserID(ctx)
	l, err := h.svc.Get(ctx, id)
	if errors.Is(err, ErrNotFound) || (err == nil && l.Status == StatusRemoved && l.SellerID != uid) {
		return errNotFound
	}
	if err != nil {
		return httpx.Internal(err)
	}

	now := h.clock.Now()
	v := NewListingView(l, now)
	if seller, err := h.users.GetForDisplay(ctx, l.SellerID); err == nil {
		pv := users.NewPublicView(seller)
		v.Seller = &pv
	}
	if v.Auction != nil && h.live != nil {
		// The current highest bid must come from Redis, never from the replica.
		if live, err := h.live.LiveAuction(ctx, l); err == nil {
			v.Auction.Open, v.Auction.EndsAt = live.Open, live.EndsAt
			v.Auction.HighestBidPaise, v.Auction.HighestBidderID = live.HighestBidPaise, live.HighestBidderID
			v.Auction.MinNextBidPaise = live.MinNextBidPaise
		} else {
			slog.WarnContext(ctx, "live auction state unavailable", "listing_id", l.ID, "err", err)
		}
	}
	if uid != l.SellerID {
		if viewer, err := h.users.GetForDisplay(ctx, uid); err == nil {
			v.ViewerEligibility = eligibilityView(l.CheckBuyer(BuyerFrom(viewer), now))
		}
	}
	httpx.WriteJSON(w, http.StatusOK, v)
	return nil
}

func eligibilityView(err error) *EligibilityView {
	if err == nil {
		return &EligibilityView{Eligible: true}
	}
	var apiErr *httpx.Error
	if errors.As(err, &apiErr) {
		return &EligibilityView{Code: apiErr.Code, Reason: apiErr.Message}
	}
	return &EligibilityView{Code: "unknown", Reason: err.Error()}
}

type createRequest struct {
	Title              string     `json:"title" validate:"required,min=3,max=120"`
	Description        string     `json:"description" validate:"max=4000"`
	Category           string     `json:"category" validate:"required"`
	Condition          string     `json:"condition" validate:"required"`
	BiddingEnabled     bool       `json:"bidding_enabled"`
	PricePaise         *int64     `json:"price_paise"`
	StartingPricePaise *int64     `json:"starting_price_paise"`
	MinIncrementPaise  *int64     `json:"min_increment_paise"`
	BidEndsAt          *time.Time `json:"bid_ends_at"`
	AvailableFrom      *string    `json:"available_from"`
	AvailableUntil     *string    `json:"available_until"`
	AllowedBlock       *string    `json:"allowed_block" validate:"omitnil,min=1,max=20"`
	AllowedYear        *int16     `json:"allowed_year" validate:"omitnil,min=1,max=6"`
	PhotoPublicIDs     []string   `json:"photo_public_ids" validate:"max=5,unique,dive,required,max=255"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var req createRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	window, err := parseWindow(req.AvailableFrom, req.AvailableUntil)
	if err != nil {
		return err
	}
	d := Draft{
		Title: req.Title, Description: req.Description, Category: req.Category, Condition: req.Condition,
		BiddingEnabled: req.BiddingEnabled, PricePaise: req.PricePaise, StartingPricePaise: req.StartingPricePaise,
		MinIncrementPaise: req.MinIncrementPaise, BidEndsAt: req.BidEndsAt,
		Rules:    Rules{Window: window, AllowedBlock: req.AllowedBlock, AllowedYear: req.AllowedYear},
		PhotoIDs: req.PhotoPublicIDs,
	}
	uid, _ := reqctx.UserID(r.Context())
	l, err := h.svc.Create(r.Context(), uid, d)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, NewListingView(l, h.clock.Now()))
	return nil
}

type patchRequest struct {
	Title              *string                `json:"title" validate:"omitnil,min=3,max=120"`
	Description        *string                `json:"description" validate:"omitnil,max=4000"`
	Category           *string                `json:"category"`
	Condition          *string                `json:"condition"`
	BiddingEnabled     *bool                  `json:"bidding_enabled"`
	PricePaise         *int64                 `json:"price_paise"`
	StartingPricePaise *int64                 `json:"starting_price_paise"`
	MinIncrementPaise  *int64                 `json:"min_increment_paise"`
	BidEndsAt          *time.Time             `json:"bid_ends_at"`
	AvailableFrom      httpx.Optional[string] `json:"available_from"`
	AvailableUntil     httpx.Optional[string] `json:"available_until"`
	AllowedBlock       httpx.Optional[string] `json:"allowed_block"`
	AllowedYear        httpx.Optional[int16]  `json:"allowed_year"`
	PhotoPublicIDs     *[]string              `json:"photo_public_ids" validate:"omitnil,max=5,unique,dive,required,max=255"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return errNotFound
	}
	var req patchRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	p := Patch{
		Title: req.Title, Description: req.Description, Category: req.Category, Condition: req.Condition,
		BiddingEnabled: req.BiddingEnabled, PricePaise: req.PricePaise, StartingPricePaise: req.StartingPricePaise,
		MinIncrementPaise: req.MinIncrementPaise, BidEndsAt: req.BidEndsAt,
		AllowedBlock: req.AllowedBlock, AllowedYear: req.AllowedYear, PhotoIDs: req.PhotoPublicIDs,
	}
	if req.AvailableFrom.Set || req.AvailableUntil.Set {
		if req.AvailableFrom.Null != req.AvailableUntil.Null || !req.AvailableFrom.Set || !req.AvailableUntil.Set {
			return httpx.ValidationFailed(httpx.FieldError{Field: "available_from", Rule: "pair",
				Message: "send available_from and available_until together (both times, or both null to clear)"})
		}
		p.Window.Set = true
		if req.AvailableFrom.Null {
			p.Window.Null = true
		} else {
			win, err := parseWindow(&req.AvailableFrom.Value, &req.AvailableUntil.Value)
			if err != nil {
				return err
			}
			p.Window.Value = *win
		}
	}
	uid, _ := reqctx.UserID(r.Context())
	l, err := h.svc.Update(r.Context(), uid, id, p)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, NewListingView(l, h.clock.Now()))
	return nil
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return errNotFound
	}
	uid, _ := reqctx.UserID(r.Context())
	if _, err := h.svc.Remove(r.Context(), uid, id, false); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// viewer loads the caller's profile (hostel scoping needs it).
func (h *Handler) viewer(ctx context.Context) (users.User, error) {
	uid, _ := reqctx.UserID(ctx)
	u, err := h.users.GetForDisplay(ctx, uid)
	if errors.Is(err, users.ErrNotFound) {
		return users.User{}, httpx.Unauthorized("account no longer exists")
	}
	if err != nil {
		return users.User{}, httpx.Internal(err)
	}
	return u, nil
}

// --- parsing ------------------------------------------------------------------

func parseWindow(from, until *string) (*Window, error) {
	if from == nil && until == nil {
		return nil, nil
	}
	if from == nil || until == nil {
		return nil, httpx.ValidationFailed(httpx.FieldError{Field: "available_from", Rule: "pair",
			Message: "available_from and available_until must be given together"})
	}
	f, err1 := ParseTimeOfDay(*from)
	u, err2 := ParseTimeOfDay(*until)
	if err1 != nil || err2 != nil {
		return nil, httpx.ValidationFailed(httpx.FieldError{Field: "available_from", Rule: "time", Message: "times must be HH:MM (24-hour, IST)"})
	}
	w, err := NewWindow(f, u)
	if err != nil {
		return nil, httpx.ValidationFailed(httpx.FieldError{Field: "available_until", Rule: "window", Message: err.Error()})
	}
	return &w, nil
}

func parseFilter(qv url.Values) (Filter, error) {
	f := Filter{Sort: SortNewest, Cursor: qv.Get("cursor")}
	limit, err := parseLimit(qv.Get("limit"))
	if err != nil {
		return Filter{}, err
	}
	f.Limit = limit

	if s := qv.Get("sort"); s != "" {
		f.Sort = Sort(s)
		if !slices.Contains([]Sort{SortNewest, SortPriceAsc, SortPriceDesc}, f.Sort) {
			return Filter{}, queryErr("sort", "must be one of: newest price_asc price_desc")
		}
	}
	if q := strings.TrimSpace(qv.Get("q")); q != "" {
		if len(q) > 100 {
			return Filter{}, queryErr("q", "must be at most 100 characters")
		}
		f.Query = &q
	}
	if c := qv.Get("category"); c != "" {
		if !slices.Contains(Categories, c) {
			return Filter{}, queryErr("category", "must be one of: "+strings.Join(Categories, " "))
		}
		f.Category = &c
	}
	if c := qv.Get("condition"); c != "" {
		if !slices.Contains(Conditions, c) {
			return Filter{}, queryErr("condition", "must be one of: "+strings.Join(Conditions, " "))
		}
		f.Condition = &c
	}
	if b := qv.Get("bidding"); b != "" {
		v, err := strconv.ParseBool(b)
		if err != nil {
			return Filter{}, queryErr("bidding", "must be true or false")
		}
		f.Bidding = &v
	}
	for name, dst := range map[string]**int64{"min_price": &f.MinPrice, "max_price": &f.MaxPrice} {
		if s := qv.Get(name); s != "" {
			v, err := strconv.ParseInt(s, 10, 64)
			if err != nil || v < 0 {
				return Filter{}, queryErr(name, "must be a non-negative amount in paise")
			}
			*dst = &v
		}
	}
	return f, nil
}

func parseLimit(s string) (int32, error) {
	if s == "" {
		return DefaultPageSize, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > MaxPageSize {
		return 0, queryErr("limit", "must be between 1 and 50")
	}
	return int32(n), nil
}

func queryErr(field, msg string) error {
	return httpx.ValidationFailed(httpx.FieldError{Field: field, Rule: "query", Message: msg})
}
