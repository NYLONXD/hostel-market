package listings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hostelmarket/internal/listings/store"
	"hostelmarket/internal/media"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/users"
)

// UserDirectory is the slice of the users module listings needs.
type UserDirectory interface {
	Get(ctx context.Context, id uuid.UUID) (users.User, error)
	GetForDisplay(ctx context.Context, id uuid.UUID) (users.User, error)
}

// Photos is the slice of the media module listings needs.
type Photos interface {
	VerifyOwned(ctx context.Context, userID uuid.UUID, publicIDs []string) ([]media.Asset, error)
	ScheduleDelete(ctx context.Context, publicIDs []string) error
}

// FeedCache caches browse pages per hostel namespace.
type FeedCache interface {
	Get(ctx context.Context, ns, key string, dst any) bool
	Set(ctx context.Context, ns, key string, v any)
	Invalidate(ctx context.Context, ns string)
}

// AuctionHooks lets the bidding module react to auction lifecycle changes made here
// (schedule/reschedule the close job, refresh live Redis state). Called after commit.
type AuctionHooks interface {
	AuctionChanged(ctx context.Context, l Listing) error
}

// RemovalHook runs inside the removal transaction (e.g. orders rejects open requests).
type RemovalHook interface {
	ListingRemoved(ctx context.Context, tx pgx.Tx, l Listing) error
}

type Service struct {
	db     *db.Router
	users  UserDirectory
	photos Photos
	feed   FeedCache
	clock  clock.Clock

	auctions     AuctionHooks
	removalHooks []RemovalHook
}

func NewService(router *db.Router, u UserDirectory, p Photos, feed FeedCache, clk clock.Clock) *Service {
	return &Service{db: router, users: u, photos: p, feed: feed, clock: clk}
}

// SetAuctionHooks is called by the bidding module during wiring.
func (s *Service) SetAuctionHooks(h AuctionHooks) { s.auctions = h }

// OnRemove registers a hook that runs inside every removal transaction.
func (s *Service) OnRemove(h RemovalHook) { s.removalHooks = append(s.removalHooks, h) }

var (
	errNotFound    = httpx.NotFound("listing not found")
	errNotOwner    = httpx.Forbidden("not_owner", "you can only change your own listings")
	errPhotoInUse  = httpx.Conflict("photo_in_use", "one of these photos is already attached to another listing")
	errNotEditable = httpx.Conflict("listing_not_editable", "only active listings can be changed")
)

// ErrNotFound is returned by the transactional helpers other modules call.
var ErrNotFound = errors.New("listings: not found")

func (s *Service) Create(ctx context.Context, sellerID uuid.UUID, d Draft) (Listing, error) {
	seller, err := s.users.Get(ctx, sellerID)
	if err != nil {
		return Listing{}, httpx.Internal(err)
	}
	switch {
	case !seller.IsVerified:
		return Listing{}, httpx.Forbidden("not_verified", "verify your account before selling")
	case seller.UPIID == nil:
		return Listing{}, httpx.Forbidden("upi_required", "add your UPI ID to your profile before selling")
	}

	d.normalize()
	if err := d.validate(s.clock.Now(), true); err != nil {
		return Listing{}, err
	}
	assets, err := s.photos.VerifyOwned(ctx, sellerID, d.PhotoIDs)
	if err != nil {
		return Listing{}, err
	}

	var l Listing
	err = s.db.WithTx(ctx, func(tx pgx.Tx) error {
		q := store.New(tx)
		from, until := windowParams(d.Rules.Window)
		row, err := q.InsertListing(ctx, store.InsertListingParams{
			SellerID:           sellerID,
			Hostel:             seller.Hostel,
			Title:              d.Title,
			Description:        d.Description,
			Category:           d.Category,
			Condition:          d.Condition,
			BiddingEnabled:     d.BiddingEnabled,
			PricePaise:         d.PricePaise,
			StartingPricePaise: d.StartingPricePaise,
			MinIncrementPaise:  d.MinIncrementPaise,
			BidEndsAt:          d.BidEndsAt,
			AvailableFrom:      from,
			AvailableUntil:     until,
			AllowedBlock:       d.Rules.AllowedBlock,
			AllowedYear:        d.Rules.AllowedYear,
		})
		if err != nil {
			return err
		}
		photos, err := replacePhotos(ctx, q, row.ID, d.PhotoIDs, assetsByID(assets))
		if err != nil {
			return err
		}
		l = fromRow(row, photos)
		return nil
	})
	if db.IsUniqueViolation(err, "listing_photos_public_id_key") {
		return Listing{}, errPhotoInUse
	}
	if err != nil {
		return Listing{}, httpx.Internal(fmt.Errorf("create listing: %w", err))
	}

	s.FeedChanged(ctx, l.Hostel)
	if l.BiddingEnabled {
		s.notifyAuction(ctx, l)
	}
	return l, nil
}

func (s *Service) Update(ctx context.Context, sellerID, id uuid.UUID, p Patch) (Listing, error) {
	// Verify only photos that aren't already on this listing (each check is a Cloudinary call).
	current, err := s.photosOf(ctx, s.db.Reader(ctx, db.Strong), id)
	if err != nil {
		return Listing{}, httpx.Internal(err)
	}
	var added []string
	if p.PhotoIDs != nil {
		for _, pid := range *p.PhotoIDs {
			if _, ok := current[pid]; !ok {
				added = append(added, pid)
			}
		}
	}
	assets, err := s.photos.VerifyOwned(ctx, sellerID, added)
	if err != nil {
		return Listing{}, err
	}

	var (
		updated        Listing
		dropped        []string
		auctionChanged bool
	)
	err = s.db.WithTx(ctx, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.GetListingForUpdate(ctx, id)
		if db.IsNoRows(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		existing := fromRow(row, sortedPhotos(current))
		switch {
		case existing.SellerID != sellerID:
			return errNotOwner
		case existing.Status != StatusActive:
			return errNotEditable
		}

		d := draftFrom(existing)
		auctionChanged = d.apply(p)
		if auctionChanged && existing.Auction != nil && existing.Auction.SettingsLocked() {
			return httpx.Conflict("bidding_locked", "bidding settings can't change after the first bid")
		}
		d.normalize()
		if err := d.validate(s.clock.Now(), auctionChanged && d.BiddingEnabled); err != nil {
			return err
		}

		params := updateParams(row, d, auctionChanged)
		row, err = q.UpdateListing(ctx, params)
		if err != nil {
			return err
		}

		photos := sortedPhotos(current)
		if p.PhotoIDs != nil {
			known := assetsByID(assets)
			for pid, ph := range current {
				known[pid] = media.Asset{PublicID: ph.PublicID, SecureURL: ph.SecureUrl,
					Width: int(deref(ph.Width)), Height: int(deref(ph.Height)), Bytes: int(deref(ph.Bytes)), Format: deref(ph.Format)}
				if !slices.Contains(d.PhotoIDs, pid) {
					dropped = append(dropped, pid)
				}
			}
			if photos, err = replacePhotos(ctx, q, id, d.PhotoIDs, known); err != nil {
				return err
			}
		}
		updated = fromRow(row, photos)
		return nil
	})
	if db.IsUniqueViolation(err, "listing_photos_public_id_key") {
		return Listing{}, errPhotoInUse
	}
	if err != nil {
		return Listing{}, asAPIError(err)
	}

	s.FeedChanged(ctx, updated.Hostel)
	if err := s.photos.ScheduleDelete(ctx, dropped); err != nil {
		slog.ErrorContext(ctx, "schedule photo deletion failed", "listing_id", id, "err", err)
	}
	if auctionChanged {
		s.notifyAuction(ctx, updated)
	}
	return updated, nil
}

// updateParams writes the merged draft. When an auction's settings change after a
// previous round ended (or the mode changes), a fresh round starts so old bids never
// mix with new ones.
func updateParams(row store.Listing, d Draft, auctionChanged bool) store.UpdateListingParams {
	from, until := windowParams(d.Rules.Window)
	p := store.UpdateListingParams{
		ID:                 row.ID,
		Title:              d.Title,
		Description:        d.Description,
		Category:           d.Category,
		Condition:          d.Condition,
		BiddingEnabled:     d.BiddingEnabled,
		PricePaise:         d.PricePaise,
		StartingPricePaise: d.StartingPricePaise,
		MinIncrementPaise:  d.MinIncrementPaise,
		BidEndsAt:          d.BidEndsAt,
		AuctionRound:       row.AuctionRound,
		AuctionClosedAt:    row.AuctionClosedAt,
		FirstBidAt:         row.FirstBidAt,
		CurrentBidPaise:    row.CurrentBidPaise,
		BidCount:           row.BidCount,
		AvailableFrom:      from,
		AvailableUntil:     until,
		AllowedBlock:       d.Rules.AllowedBlock,
		AllowedYear:        d.Rules.AllowedYear,
	}
	if auctionChanged && (row.AuctionClosedAt != nil || row.BidCount > 0) {
		p.AuctionRound++
		p.AuctionClosedAt, p.FirstBidAt, p.CurrentBidPaise, p.BidCount = nil, nil, nil, 0
	}
	return p
}

// Remove marks a listing removed and schedules its photos for deletion. Sellers can't
// remove an auction that already has bids; admins (asAdmin) can.
func (s *Service) Remove(ctx context.Context, actorID, id uuid.UUID, asAdmin bool) (Listing, error) {
	var removed Listing
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.GetListingForUpdate(ctx, id)
		if db.IsNoRows(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		l := fromRow(row, nil)
		switch {
		case !asAdmin && l.SellerID != actorID:
			return errNotOwner
		case l.Status != StatusActive:
			return httpx.Conflict("listing_not_removable", "only active listings can be removed")
		case !asAdmin && l.Auction != nil && l.Auction.SettingsLocked():
			return httpx.Conflict("auction_in_progress", "an auction with bids can only be removed by an admin")
		}
		if row, err = q.SetListingStatus(ctx, store.SetListingStatusParams{ID: id, Status: string(StatusRemoved)}); err != nil {
			return err
		}
		photos, err := q.ListPhotos(ctx, id)
		if err != nil {
			return err
		}
		removed = fromRow(row, photos)
		for _, h := range s.removalHooks {
			if err := h.ListingRemoved(ctx, tx, removed); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Listing{}, asAPIError(err)
	}

	s.FeedChanged(ctx, removed.Hostel)
	if err := s.photos.ScheduleDelete(ctx, removed.PhotoIDs()); err != nil {
		slog.ErrorContext(ctx, "schedule photo deletion failed", "listing_id", id, "err", err)
	}
	if removed.BiddingEnabled {
		s.notifyAuction(ctx, removed)
	}
	return removed, nil
}

// Get returns a listing for display. It may read the replica.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Listing, error) {
	return s.get(ctx, s.db.Reader(ctx, db.ReplicaOK), id)
}

// GetStrong reads from the primary; use before acting on availability.
func (s *Service) GetStrong(ctx context.Context, id uuid.UUID) (Listing, error) {
	return s.get(ctx, s.db.Reader(ctx, db.Strong), id)
}

func (s *Service) get(ctx context.Context, conn db.DBTX, id uuid.UUID) (Listing, error) {
	q := store.New(conn)
	row, err := q.GetListing(ctx, id)
	if db.IsNoRows(err) {
		return Listing{}, ErrNotFound
	}
	if err != nil {
		return Listing{}, fmt.Errorf("get listing: %w", err)
	}
	photos, err := q.ListPhotos(ctx, id)
	if err != nil {
		return Listing{}, fmt.Errorf("list photos: %w", err)
	}
	return fromRow(row, photos), nil
}

// Lock loads a listing with SELECT ... FOR UPDATE inside the caller's transaction.
// Every module that changes a listing's availability (orders, bidding) goes through it,
// so concurrent buyers serialise on the row.
func (s *Service) Lock(ctx context.Context, tx db.DBTX, id uuid.UUID) (Listing, error) {
	row, err := store.New(tx).GetListingForUpdate(ctx, id)
	if db.IsNoRows(err) {
		return Listing{}, ErrNotFound
	}
	if err != nil {
		return Listing{}, fmt.Errorf("lock listing: %w", err)
	}
	return fromRow(row, nil), nil
}

// SetStatus changes status inside the caller's transaction (which should hold Lock).
// Call FeedChanged after the transaction commits.
func (s *Service) SetStatus(ctx context.Context, tx db.DBTX, id uuid.UUID, status Status) (Listing, error) {
	row, err := store.New(tx).SetListingStatus(ctx, store.SetListingStatusParams{ID: id, Status: string(status)})
	if db.IsNoRows(err) {
		return Listing{}, ErrNotFound
	}
	if err != nil {
		return Listing{}, fmt.Errorf("set listing status: %w", err)
	}
	return fromRow(row, nil), nil
}

// ApplyBid records an accepted bid (current price, count, first-bid lock, extended end
// time) inside the caller's transaction, which must hold Lock.
func (s *Service) ApplyBid(ctx context.Context, tx db.DBTX, id uuid.UUID, amount int64, endsAt time.Time) (Listing, error) {
	row, err := store.New(tx).ApplyBid(ctx, store.ApplyBidParams{ID: id, Amount: amount, EndsAt: endsAt})
	if err != nil {
		return Listing{}, fmt.Errorf("apply bid: %w", err)
	}
	return fromRow(row, nil), nil
}

// CloseAuctionRound stamps auction_closed_at; it reports false if the round was
// already closed (making the close job idempotent).
func (s *Service) CloseAuctionRound(ctx context.Context, tx db.DBTX, id uuid.UUID) (Listing, bool, error) {
	row, err := store.New(tx).CloseAuctionRound(ctx, id)
	if db.IsNoRows(err) {
		return Listing{}, false, nil
	}
	if err != nil {
		return Listing{}, false, fmt.Errorf("close auction round: %w", err)
	}
	return fromRow(row, nil), true, nil
}

// OpenAuctions lists running auctions from the primary (startup rebuild, sweeps).
func (s *Service) OpenAuctions(ctx context.Context, limit int32) ([]Listing, error) {
	rows, err := store.New(s.db.Reader(ctx, db.Strong)).ListOpenAuctions(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list open auctions: %w", err)
	}
	out := make([]Listing, len(rows))
	for i, r := range rows {
		out[i] = fromRow(r, nil)
	}
	return out, nil
}

// DueAuction identifies an auction round whose end time has passed.
type DueAuction struct {
	ID    uuid.UUID
	Round int32
}

func (s *Service) DueAuctions(ctx context.Context) ([]DueAuction, error) {
	rows, err := store.New(s.db.Reader(ctx, db.Strong)).ListDueAuctions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list due auctions: %w", err)
	}
	out := make([]DueAuction, len(rows))
	for i, r := range rows {
		out[i] = DueAuction{ID: r.ID, Round: r.AuctionRound}
	}
	return out, nil
}

// FeedChanged invalidates the cached feed of a hostel.
func (s *Service) FeedChanged(ctx context.Context, hostel string) {
	s.feed.Invalidate(ctx, feedNamespace(hostel))
}

// notifyAuction tells the bidding module an auction was created, re-configured, switched
// to fixed price or removed; it decides what that means for its Redis state and jobs.
func (s *Service) notifyAuction(ctx context.Context, l Listing) {
	if s.auctions == nil {
		return
	}
	if err := s.auctions.AuctionChanged(ctx, l); err != nil {
		// The bidding sweeper re-syncs auctions periodically, so this is recoverable.
		slog.ErrorContext(ctx, "auction hook failed", "listing_id", l.ID, "err", err)
	}
}

func (s *Service) photosOf(ctx context.Context, conn db.DBTX, id uuid.UUID) (map[string]store.ListingPhoto, error) {
	rows, err := store.New(conn).ListPhotos(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make(map[string]store.ListingPhoto, len(rows))
	for _, r := range rows {
		out[r.PublicID] = r
	}
	return out, nil
}

func replacePhotos(ctx context.Context, q *store.Queries, listingID uuid.UUID, ids []string, assets map[string]media.Asset) ([]store.ListingPhoto, error) {
	if err := q.DeletePhotos(ctx, listingID); err != nil {
		return nil, err
	}
	out := make([]store.ListingPhoto, 0, len(ids))
	for i, pid := range ids {
		a, ok := assets[pid]
		if !ok {
			return nil, fmt.Errorf("photo %q was not verified", pid)
		}
		p := store.InsertPhotoParams{
			ListingID: listingID,
			PublicID:  a.PublicID,
			SecureUrl: a.SecureURL,
			Width:     ptrInt32(a.Width),
			Height:    ptrInt32(a.Height),
			Bytes:     ptrInt32(a.Bytes),
			Format:    &a.Format,
			Position:  int16(i),
		}
		if err := q.InsertPhoto(ctx, p); err != nil {
			return nil, err
		}
		out = append(out, store.ListingPhoto{ListingID: listingID, PublicID: p.PublicID, SecureUrl: p.SecureUrl,
			Width: p.Width, Height: p.Height, Bytes: p.Bytes, Format: p.Format, Position: p.Position})
	}
	return out, nil
}

func assetsByID(assets []media.Asset) map[string]media.Asset {
	m := make(map[string]media.Asset, len(assets))
	for _, a := range assets {
		m[a.PublicID] = a
	}
	return m
}

func sortedPhotos(m map[string]store.ListingPhoto) []store.ListingPhoto {
	out := make([]store.ListingPhoto, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b store.ListingPhoto) int { return int(a.Position) - int(b.Position) })
	return out
}

func ptrInt32(v int) *int32 {
	x := int32(v)
	return &x
}

func asAPIError(err error) error {
	var apiErr *httpx.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return httpx.Internal(err)
}
