package listings

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/users"
)

const minutesPerDay = 24 * 60

// TimeOfDay is a wall-clock time in IST, as minutes after midnight (0..1439).
type TimeOfDay int16

// ParseTimeOfDay accepts "HH:MM" (24-hour).
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("invalid time %q, want HH:MM (24-hour)", s)
	}
	return TimeOfDay(t.Hour()*60 + t.Minute()), nil
}

func (t TimeOfDay) String() string { return fmt.Sprintf("%02d:%02d", t/60, t%60) }

// ClockTime converts an instant to its IST time of day.
func ClockTime(t time.Time) TimeOfDay {
	ist := t.In(clock.IST)
	return TimeOfDay(ist.Hour()*60 + ist.Minute())
}

// Window is an availability window [From, Until). From > Until wraps past
// midnight, so 22:00-06:00 covers late night and early morning.
type Window struct {
	From  TimeOfDay
	Until TimeOfDay
}

func NewWindow(from, until TimeOfDay) (Window, error) {
	if from < 0 || from >= minutesPerDay || until < 0 || until >= minutesPerDay {
		return Window{}, fmt.Errorf("time out of range")
	}
	if from == until {
		return Window{}, fmt.Errorf("available_from and available_until must differ")
	}
	return Window{From: from, Until: until}, nil
}

func (w Window) Contains(t TimeOfDay) bool {
	if w.From < w.Until {
		return t >= w.From && t < w.Until
	}
	return t >= w.From || t < w.Until
}

// Rules are the seller's optional restrictions on who may buy or bid.
type Rules struct {
	Window       *Window
	AllowedBlock *string // canonical (users.NormalizeLabel) block name
	AllowedYear  *int16
}

// Buyer is the subset of a user that eligibility depends on.
type Buyer struct {
	ID       uuid.UUID
	Hostel   string
	Block    string
	Year     int16
	Verified bool
}

func BuyerFrom(u users.User) Buyer {
	return Buyer{ID: u.ID, Hostel: u.Hostel, Block: u.Block, Year: u.Year, Verified: u.IsVerified}
}

// CheckBuyer enforces every rule for buying or bidding at instant now. Callers must pass
// data read from the primary; the returned error is a 403 (or 409) explaining why.
func (l Listing) CheckBuyer(b Buyer, now time.Time) error {
	switch {
	case l.Status != StatusActive:
		return httpx.Conflict("listing_unavailable", "this listing is no longer available")
	case b.ID == l.SellerID:
		return httpx.Forbidden("own_listing", "you cannot buy or bid on your own listing")
	case !b.Verified:
		return httpx.Forbidden("not_verified", "verify your account before buying or bidding")
	case !strings.EqualFold(b.Hostel, l.Hostel):
		return httpx.Forbidden("different_hostel", "this listing is only open to residents of "+l.Hostel)
	}
	return l.Rules.Check(b, now)
}

// Check applies the seller-defined rules (block, year, time window).
func (r Rules) Check(b Buyer, now time.Time) error {
	if r.AllowedBlock != nil && !strings.EqualFold(b.Block, *r.AllowedBlock) {
		return httpx.Forbidden("block_restricted", "only residents of block "+*r.AllowedBlock+" can buy this listing")
	}
	if r.AllowedYear != nil && b.Year != *r.AllowedYear {
		return httpx.Forbidden("year_restricted", fmt.Sprintf("only year %d students can buy this listing", *r.AllowedYear))
	}
	if r.Window != nil {
		if t := ClockTime(now); !r.Window.Contains(t) {
			return httpx.Forbidden("outside_time_window", fmt.Sprintf(
				"this listing can only be bought between %s and %s IST (it is %s IST now)", r.Window.From, r.Window.Until, t))
		}
	}
	return nil
}
