package listings

import (
	"slices"
	"strings"
	"time"

	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/money"
	"hostelmarket/internal/users"
)

// Draft is the full editable state of a listing, used for create and (after merging a
// patch onto the stored listing) update, so both paths share one validator.
type Draft struct {
	Title              string
	Description        string
	Category           string
	Condition          string
	BiddingEnabled     bool
	PricePaise         *int64
	StartingPricePaise *int64
	MinIncrementPaise  *int64
	BidEndsAt          *time.Time
	Rules              Rules
	PhotoIDs           []string
}

// Patch carries optional changes. Optional fields distinguish "clear" (null) from "keep".
type Patch struct {
	Title              *string
	Description        *string
	Category           *string
	Condition          *string
	BiddingEnabled     *bool
	PricePaise         *int64
	StartingPricePaise *int64
	MinIncrementPaise  *int64
	BidEndsAt          *time.Time
	Window             httpx.Optional[Window]
	AllowedBlock       httpx.Optional[string]
	AllowedYear        httpx.Optional[int16]
	PhotoIDs           *[]string
}

func draftFrom(l Listing) Draft {
	d := Draft{
		Title: l.Title, Description: l.Description, Category: l.Category, Condition: l.Condition,
		BiddingEnabled: l.BiddingEnabled, PricePaise: l.PricePaise, Rules: l.Rules, PhotoIDs: l.PhotoIDs(),
	}
	if a := l.Auction; a != nil {
		d.StartingPricePaise = &a.StartingPricePaise
		d.MinIncrementPaise = &a.MinIncrementPaise
		d.BidEndsAt = &a.EndsAt
	}
	return d
}

// apply merges p into d and reports whether any bidding setting changed.
func (d *Draft) apply(p Patch) (auctionChanged bool) {
	before := *d
	if p.BiddingEnabled != nil && *p.BiddingEnabled != d.BiddingEnabled {
		// Switching mode drops the old mode's fields; the patch must supply the new ones.
		d.BiddingEnabled = *p.BiddingEnabled
		if d.BiddingEnabled {
			d.PricePaise = nil
		} else {
			d.StartingPricePaise, d.MinIncrementPaise, d.BidEndsAt = nil, nil, nil
		}
	}
	setIf(&d.Title, p.Title)
	setIf(&d.Description, p.Description)
	setIf(&d.Category, p.Category)
	setIf(&d.Condition, p.Condition)
	if p.PricePaise != nil {
		d.PricePaise = p.PricePaise
	}
	if p.StartingPricePaise != nil {
		d.StartingPricePaise = p.StartingPricePaise
	}
	if p.MinIncrementPaise != nil {
		d.MinIncrementPaise = p.MinIncrementPaise
	}
	if p.BidEndsAt != nil {
		d.BidEndsAt = p.BidEndsAt
	}
	if p.Window.Set {
		d.Rules.Window = p.Window.Ptr()
	}
	if p.AllowedBlock.Set {
		d.Rules.AllowedBlock = p.AllowedBlock.Ptr()
	}
	if p.AllowedYear.Set {
		d.Rules.AllowedYear = p.AllowedYear.Ptr()
	}
	if p.PhotoIDs != nil {
		d.PhotoIDs = *p.PhotoIDs
	}
	return before.BiddingEnabled != d.BiddingEnabled ||
		!eqPtr(before.StartingPricePaise, d.StartingPricePaise) ||
		!eqPtr(before.MinIncrementPaise, d.MinIncrementPaise) ||
		!eqTimePtr(before.BidEndsAt, d.BidEndsAt)
}

func (d *Draft) normalize() {
	d.Title = strings.TrimSpace(d.Title)
	d.Description = strings.TrimSpace(d.Description)
	if d.Rules.AllowedBlock != nil {
		b := users.NormalizeLabel(*d.Rules.AllowedBlock)
		d.Rules.AllowedBlock = &b
	}
	if d.BidEndsAt != nil {
		t := d.BidEndsAt.UTC().Truncate(time.Second)
		d.BidEndsAt = &t
	}
}

// validate checks cross-field rules that struct tags can't. checkEndTime is false when
// an untouched auction end time is merely carried over (it may be close to now).
func (d Draft) validate(now time.Time, checkEndTime bool) error {
	var errs []httpx.FieldError
	add := func(field, rule, msg string) {
		errs = append(errs, httpx.FieldError{Field: field, Rule: rule, Message: msg})
	}
	amount := func(field string, v *int64) {
		if v != nil && (*v <= 0 || *v > money.MaxPaise) {
			add(field, "range", "must be between 1 and 1000000000 paise")
		}
	}

	if n := len([]rune(d.Title)); n < 3 || n > 120 {
		add("title", "len", "must be 3-120 characters")
	}
	if !slices.Contains(Categories, d.Category) {
		add("category", "oneof", "must be one of: "+strings.Join(Categories, " "))
	}
	if !slices.Contains(Conditions, d.Condition) {
		add("condition", "oneof", "must be one of: "+strings.Join(Conditions, " "))
	}

	if d.BiddingEnabled {
		if d.PricePaise != nil {
			add("price_paise", "excluded", "must not be set when bidding is enabled; use starting_price_paise")
		}
		if d.StartingPricePaise == nil {
			add("starting_price_paise", "required", "is required when bidding is enabled")
		}
		if d.MinIncrementPaise == nil {
			add("min_increment_paise", "required", "is required when bidding is enabled")
		}
		switch {
		case d.BidEndsAt == nil:
			add("bid_ends_at", "required", "is required when bidding is enabled")
		case checkEndTime && d.BidEndsAt.Before(now.Add(MinAuctionDuration)):
			add("bid_ends_at", "min", "must be at least 10 minutes from now")
		case checkEndTime && d.BidEndsAt.After(now.Add(MaxAuctionDuration)):
			add("bid_ends_at", "max", "must be within 14 days from now")
		}
	} else {
		if d.PricePaise == nil {
			add("price_paise", "required", "is required for a fixed-price listing")
		}
		if d.StartingPricePaise != nil || d.MinIncrementPaise != nil || d.BidEndsAt != nil {
			add("bidding_enabled", "required", "starting_price_paise, min_increment_paise and bid_ends_at need bidding_enabled=true")
		}
	}
	amount("price_paise", d.PricePaise)
	amount("starting_price_paise", d.StartingPricePaise)
	amount("min_increment_paise", d.MinIncrementPaise)

	if d.Rules.AllowedYear != nil && (*d.Rules.AllowedYear < 1 || *d.Rules.AllowedYear > 6) {
		add("allowed_year", "range", "must be between 1 and 6")
	}
	if d.Rules.AllowedBlock != nil && (*d.Rules.AllowedBlock == "" || len(*d.Rules.AllowedBlock) > 20) {
		add("allowed_block", "len", "must be 1-20 characters")
	}

	if len(d.PhotoIDs) > MaxPhotos {
		add("photo_public_ids", "max", "at most 5 photos per listing")
	}
	seen := map[string]bool{}
	for _, id := range d.PhotoIDs {
		if id == "" || seen[id] {
			add("photo_public_ids", "unique", "photo IDs must be non-empty and unique")
			break
		}
		seen[id] = true
	}

	if len(errs) > 0 {
		return httpx.ValidationFailed(errs...)
	}
	return nil
}

func setIf[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

func eqPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func eqTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
