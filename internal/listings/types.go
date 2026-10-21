// Package listings owns items for sale: CRUD, photos, the browse feed (served from the
// replica and cached in Redis) and the eligibility rules enforced on every bid and purchase.
package listings

import (
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusReserved Status = "reserved" // a live order exists (accepted, awaiting payment, ...)
	StatusSold     Status = "sold"
	StatusRemoved  Status = "removed"
)

const (
	MaxPhotos          = 5
	MinAuctionDuration = 10 * time.Minute
	MaxAuctionDuration = 14 * 24 * time.Hour
)

var (
	Categories = []string{"books", "cycles", "electronics", "appliances", "furniture", "clothing", "sports", "stationery", "other"}
	Conditions = []string{"new", "like_new", "good", "fair", "poor"}
)

type Listing struct {
	ID                uuid.UUID
	SellerID          uuid.UUID
	Hostel            string
	Title             string
	Description       string
	Category          string
	Condition         string
	Status            Status
	BiddingEnabled    bool
	PricePaise        *int64   // fixed-price listings only
	Auction           *Auction // bidding listings only
	Rules             Rules
	DisplayPricePaise int64
	Photos            []Photo
	CreatedAt         time.Time
	UpdatedAt         time.Time
	RemovedAt         *time.Time
}

// Auction is the bidding configuration plus the denormalised state of the current round.
// The live highest bid is in Redis (package bidding); CurrentBidPaise here can lag.
type Auction struct {
	StartingPricePaise int64
	MinIncrementPaise  int64
	EndsAt             time.Time
	Round              int32
	ClosedAt           *time.Time
	FirstBidAt         *time.Time
	CurrentBidPaise    *int64
	BidCount           int32
}

// SettingsLocked: bidding settings can't change once the open round has a bid.
func (a Auction) SettingsLocked() bool { return a.FirstBidAt != nil && a.ClosedAt == nil }

// MinNextBid is the smallest acceptable next bid given the stored state.
func (a Auction) MinNextBid() int64 {
	if a.CurrentBidPaise == nil {
		return a.StartingPricePaise
	}
	return *a.CurrentBidPaise + a.MinIncrementPaise
}

// LiveAuction is the real-time auction state, read from Redis by the bidding module.
type LiveAuction struct {
	Open            bool
	EndsAt          time.Time
	HighestBidPaise *int64
	HighestBidderID *uuid.UUID
	MinNextBidPaise int64
}

type Photo struct {
	PublicID string
	URL      string
	Width    int32
	Height   int32
	Position int16
}

// PhotoIDs returns the public IDs in display order.
func (l Listing) PhotoIDs() []string {
	ids := make([]string, len(l.Photos))
	for i, p := range l.Photos {
		ids[i] = p.PublicID
	}
	return ids
}
