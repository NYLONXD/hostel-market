package listings

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"hostelmarket/internal/listings/store"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/httpx"
)

type Sort string

const (
	SortNewest    Sort = "newest"
	SortPriceAsc  Sort = "price_asc"
	SortPriceDesc Sort = "price_desc"
)

const (
	DefaultPageSize = 20
	MaxPageSize     = 50
)

// Filter is a browse request. Hostel is always the viewer's own hostel.
type Filter struct {
	Hostel    string  `json:"h"`
	Query     *string `json:"q,omitempty"`
	Category  *string `json:"c,omitempty"`
	Condition *string `json:"cd,omitempty"`
	Bidding   *bool   `json:"b,omitempty"`
	MinPrice  *int64  `json:"min,omitempty"`
	MaxPrice  *int64  `json:"max,omitempty"`
	Sort      Sort    `json:"s"`
	Cursor    string  `json:"cur,omitempty"`
	Limit     int32   `json:"l"`
}

// Card is a feed item: enough to render a tile without loading the listing.
type Card struct {
	ID                 uuid.UUID  `json:"id"`
	Title              string     `json:"title"`
	Category           string     `json:"category"`
	Condition          string     `json:"condition"`
	Status             Status     `json:"status"`
	BiddingEnabled     bool       `json:"bidding_enabled"`
	DisplayPricePaise  int64      `json:"display_price_paise"`
	PricePaise         *int64     `json:"price_paise,omitempty"`
	StartingPricePaise *int64     `json:"starting_price_paise,omitempty"`
	CurrentBidPaise    *int64     `json:"current_bid_paise,omitempty"`
	BidCount           int32      `json:"bid_count,omitempty"`
	BidEndsAt          *time.Time `json:"bid_ends_at,omitempty"`
	CoverURL           string     `json:"cover_url,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
}

type Page struct {
	Items      []Card `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// cursor is the keyset position after the last item of a page.
type cursor struct {
	Sort      Sort      `json:"s"`
	CreatedAt time.Time `json:"t,omitzero"`
	Price     int64     `json:"p,omitempty"`
	ID        uuid.UUID `json:"i"`
}

func (c cursor) encode() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

var errBadCursor = httpx.BadRequest("invalid_cursor", "cursor is invalid or belongs to a different sort order")

func decodeCursor(s string, want Sort) (*cursor, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errBadCursor
	}
	var c cursor
	if json.Unmarshal(b, &c) != nil || c.Sort != want || c.ID == uuid.Nil {
		return nil, errBadCursor
	}
	return &c, nil
}

func feedNamespace(hostel string) string { return "feed:" + hostel }

func (f Filter) cacheKey() string {
	b, _ := json.Marshal(f)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// Browse returns one page of the viewer's hostel feed. Pages come from the Redis cache
// when possible, otherwise from the replica. A user who just wrote skips both, so they
// always see their own change.
func (s *Service) Browse(ctx context.Context, f Filter) (Page, error) {
	cur, err := decodeCursor(f.Cursor, f.Sort)
	if err != nil {
		return Page{}, err
	}
	ns, key := feedNamespace(f.Hostel), f.cacheKey()
	useCache := !s.db.RecentlyWrote(ctx)
	if useCache {
		var cached Page
		if s.feed.Get(ctx, ns, key, &cached) {
			return cached, nil
		}
	}

	rows, err := s.queryFeed(ctx, f, cur)
	if err != nil {
		return Page{}, httpx.Internal(err)
	}
	page := buildPage(rows, f.Limit, f.Sort)
	if useCache {
		s.feed.Set(ctx, ns, key, page)
	}
	return page, nil
}

func (s *Service) queryFeed(ctx context.Context, f Filter, cur *cursor) ([]store.BrowseNewestRow, error) {
	q := store.New(s.db.Reader(ctx, db.ReplicaOK))
	limit := f.Limit + 1 // one extra row tells us whether a next page exists

	if f.Sort == SortNewest {
		p := store.BrowseNewestParams{
			Hostel: f.Hostel, Category: f.Category, Condition: f.Condition, Bidding: f.Bidding,
			MinPrice: f.MinPrice, MaxPrice: f.MaxPrice, Query: f.Query, RowLimit: limit,
		}
		if cur != nil {
			p.AfterCreated, p.AfterID = &cur.CreatedAt, &cur.ID
		}
		return q.BrowseNewest(ctx, p)
	}

	p := store.BrowsePriceAscParams{
		Hostel: f.Hostel, Category: f.Category, Condition: f.Condition, Bidding: f.Bidding,
		MinPrice: f.MinPrice, MaxPrice: f.MaxPrice, Query: f.Query, RowLimit: limit,
	}
	if cur != nil {
		p.AfterPrice, p.AfterID = &cur.Price, &cur.ID
	}
	if f.Sort == SortPriceAsc {
		r, err := q.BrowsePriceAsc(ctx, p)
		return convertRows(r, err, func(x store.BrowsePriceAscRow) store.BrowseNewestRow { return store.BrowseNewestRow(x) })
	}
	r, err := q.BrowsePriceDesc(ctx, store.BrowsePriceDescParams(p))
	return convertRows(r, err, func(x store.BrowsePriceDescRow) store.BrowseNewestRow { return store.BrowseNewestRow(x) })
}

// MineFilter pages the seller's own listings (any status).
type MineFilter struct {
	Status *Status
	Cursor string
	Limit  int32
}

func (s *Service) ListMine(ctx context.Context, sellerID uuid.UUID, f MineFilter) (Page, error) {
	cur, err := decodeCursor(f.Cursor, SortNewest)
	if err != nil {
		return Page{}, err
	}
	p := store.ListBySellerParams{SellerID: sellerID, RowLimit: f.Limit + 1}
	if f.Status != nil {
		st := string(*f.Status)
		p.Status = &st
	}
	if cur != nil {
		p.AfterCreated, p.AfterID = &cur.CreatedAt, &cur.ID
	}
	rows, err := store.New(s.db.Reader(ctx, db.ReplicaOK)).ListBySeller(ctx, p)
	if err != nil {
		return Page{}, httpx.Internal(err)
	}
	conv := make([]store.BrowseNewestRow, len(rows))
	for i, r := range rows {
		conv[i] = store.BrowseNewestRow(r)
	}
	return buildPage(conv, f.Limit, SortNewest), nil
}

func buildPage(rows []store.BrowseNewestRow, limit int32, sort Sort) Page {
	page := Page{Items: make([]Card, 0, min(len(rows), int(limit)))}
	for i, r := range rows {
		if i == int(limit) {
			last := page.Items[len(page.Items)-1]
			page.NextCursor = cursor{Sort: sort, CreatedAt: last.CreatedAt, Price: last.DisplayPricePaise, ID: last.ID}.encode()
			break
		}
		l := fromRow(r.Listing, nil)
		c := Card{
			ID: l.ID, Title: l.Title, Category: l.Category, Condition: l.Condition, Status: l.Status,
			BiddingEnabled: l.BiddingEnabled, DisplayPricePaise: l.DisplayPricePaise, PricePaise: l.PricePaise,
			CoverURL: r.CoverUrl, CreatedAt: l.CreatedAt,
		}
		if a := l.Auction; a != nil {
			c.StartingPricePaise, c.CurrentBidPaise, c.BidCount, c.BidEndsAt = &a.StartingPricePaise, a.CurrentBidPaise, a.BidCount, &a.EndsAt
		}
		page.Items = append(page.Items, c)
	}
	return page
}

func convertRows[T any](rows []T, err error, f func(T) store.BrowseNewestRow) ([]store.BrowseNewestRow, error) {
	if err != nil {
		return nil, fmt.Errorf("browse feed: %w", err)
	}
	out := make([]store.BrowseNewestRow, len(rows))
	for i, r := range rows {
		out[i] = f(r)
	}
	return out, nil
}
