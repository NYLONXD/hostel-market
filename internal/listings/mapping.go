package listings

import (
	"github.com/jackc/pgx/v5/pgtype"

	"hostelmarket/internal/listings/store"
)

const microsPerMinute = 60_000_000

func fromRow(r store.Listing, photos []store.ListingPhoto) Listing {
	l := Listing{
		ID:             r.ID,
		SellerID:       r.SellerID,
		Hostel:         r.Hostel,
		Title:          r.Title,
		Description:    r.Description,
		Category:       r.Category,
		Condition:      r.Condition,
		Status:         Status(r.Status),
		BiddingEnabled: r.BiddingEnabled,
		PricePaise:     r.PricePaise,
		Rules: Rules{
			AllowedBlock: r.AllowedBlock,
			AllowedYear:  r.AllowedYear,
		},
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
		RemovedAt: r.RemovedAt,
	}
	if r.DisplayPricePaise != nil {
		l.DisplayPricePaise = *r.DisplayPricePaise
	}
	if r.AvailableFrom.Valid && r.AvailableUntil.Valid {
		l.Rules.Window = &Window{From: fromPGTime(r.AvailableFrom), Until: fromPGTime(r.AvailableUntil)}
	}
	if r.BiddingEnabled && r.StartingPricePaise != nil && r.MinIncrementPaise != nil && r.BidEndsAt != nil {
		l.Auction = &Auction{
			StartingPricePaise: *r.StartingPricePaise,
			MinIncrementPaise:  *r.MinIncrementPaise,
			EndsAt:             *r.BidEndsAt,
			Round:              r.AuctionRound,
			ClosedAt:           r.AuctionClosedAt,
			FirstBidAt:         r.FirstBidAt,
			CurrentBidPaise:    r.CurrentBidPaise,
			BidCount:           r.BidCount,
		}
	}
	l.Photos = make([]Photo, 0, len(photos))
	for _, p := range photos {
		l.Photos = append(l.Photos, Photo{
			PublicID: p.PublicID, URL: p.SecureUrl, Width: deref(p.Width), Height: deref(p.Height), Position: p.Position,
		})
	}
	return l
}

func fromPGTime(t pgtype.Time) TimeOfDay { return TimeOfDay(t.Microseconds / microsPerMinute) }

func toPGTime(t TimeOfDay) pgtype.Time {
	return pgtype.Time{Microseconds: int64(t) * microsPerMinute, Valid: true}
}

func windowParams(w *Window) (from, until pgtype.Time) {
	if w == nil {
		return pgtype.Time{}, pgtype.Time{}
	}
	return toPGTime(w.From), toPGTime(w.Until)
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
