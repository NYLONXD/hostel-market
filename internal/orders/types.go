// Package orders runs the order state machine for both sale modes. Fixed-price orders
// are paid buyer -> seller directly (UPI or cash, no platform fee); auction orders are
// created when an auction closes and are paid to the platform.
package orders

import (
	"slices"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusRequested       Status = "requested"
	StatusAccepted        Status = "accepted"
	StatusAwaitingPayment Status = "awaiting_payment"
	StatusPaid            Status = "paid"
	StatusHandedOver      Status = "handed_over"
	StatusCompleted       Status = "completed"
	StatusRejected        Status = "rejected"
	StatusCancelled       Status = "cancelled"
	StatusExpired         Status = "expired"
)

type Kind string

const (
	KindFixed   Kind = "fixed"
	KindAuction Kind = "auction"
)

type PaymentMethod string

const (
	MethodUPIDirect PaymentMethod = "upi_direct" // buyer pays the seller's UPI ID
	MethodCash      PaymentMethod = "cash"       // cash on handover
	MethodPlatform  PaymentMethod = "platform"   // auction: buyer pays the platform
)

var AllStatuses = []Status{
	StatusRequested, StatusAccepted, StatusAwaitingPayment, StatusPaid, StatusHandedOver,
	StatusCompleted, StatusRejected, StatusCancelled, StatusExpired,
}

// transitions is the whole state machine. Terminal states have no entry.
var transitions = map[Status][]Status{
	StatusRequested:       {StatusAccepted, StatusRejected, StatusCancelled, StatusExpired},
	StatusAccepted:        {StatusAwaitingPayment, StatusCancelled},
	StatusAwaitingPayment: {StatusPaid, StatusCancelled, StatusExpired},
	StatusPaid:            {StatusHandedOver},
	StatusHandedOver:      {StatusCompleted},
}

// CanTransition reports whether from -> to is a legal move.
func CanTransition(from, to Status) bool { return slices.Contains(transitions[from], to) }

// Live statuses hold the item for this buyer (mirrors orders_one_live_per_listing).
func (s Status) Live() bool {
	switch s {
	case StatusAccepted, StatusAwaitingPayment, StatusPaid, StatusHandedOver, StatusCompleted:
		return true
	}
	return false
}

type Order struct {
	ID                uuid.UUID
	ListingID         uuid.UUID
	BuyerID           uuid.UUID
	SellerID          uuid.UUID
	Kind              Kind
	Status            Status
	PricePaise        int64
	PlatformFeeBps    int32
	PlatformFeePaise  int64
	SellerPayoutPaise int64
	PaymentMethod     *PaymentMethod
	AuctionRound      *int32
	WinningBidID      *int64
	BuyerNote         *string
	RequestExpiresAt  *time.Time
	PaymentDueAt      *time.Time
	AcceptedAt        *time.Time
	PaidAt            *time.Time
	HandedOverAt      *time.Time
	CompletedAt       *time.Time
	ClosedAt          *time.Time
	CloseReason       *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (o Order) IsParticipant(id uuid.UUID) bool { return id == o.BuyerID || id == o.SellerID }
