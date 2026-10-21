package listings

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hostelmarket/internal/platform/httpx"
)

func ptr[T any](v T) *T { return &v }

var now = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func fixedDraft() Draft {
	return Draft{Title: "Cycle", Category: "cycles", Condition: "good", PricePaise: ptr[int64](250_000)}
}

func auctionDraft() Draft {
	return Draft{Title: "Guitar", Category: "other", Condition: "good", BiddingEnabled: true,
		StartingPricePaise: ptr[int64](100_000), MinIncrementPaise: ptr[int64](5_000), BidEndsAt: ptr(now.Add(24 * time.Hour))}
}

func fieldsOf(t *testing.T, err error) []string {
	t.Helper()
	var apiErr *httpx.Error
	require.ErrorAs(t, err, &apiErr)
	var out []string
	for _, f := range apiErr.Details.([]httpx.FieldError) {
		out = append(out, f.Field)
	}
	return out
}

func TestDraftValidate(t *testing.T) {
	assert.NoError(t, fixedDraft().validate(now, true))
	assert.NoError(t, auctionDraft().validate(now, true))

	tests := []struct {
		name  string
		draft func() Draft
		field string
	}{
		{"fixed without price", func() Draft { d := fixedDraft(); d.PricePaise = nil; return d }, "price_paise"},
		{"fixed with auction fields", func() Draft { d := fixedDraft(); d.MinIncrementPaise = ptr[int64](1); return d }, "bidding_enabled"},
		{"auction with fixed price", func() Draft { d := auctionDraft(); d.PricePaise = ptr[int64](1); return d }, "price_paise"},
		{"auction missing start price", func() Draft { d := auctionDraft(); d.StartingPricePaise = nil; return d }, "starting_price_paise"},
		{"auction ends too soon", func() Draft { d := auctionDraft(); d.BidEndsAt = ptr(now.Add(time.Minute)); return d }, "bid_ends_at"},
		{"auction ends too late", func() Draft { d := auctionDraft(); d.BidEndsAt = ptr(now.Add(15 * 24 * time.Hour)); return d }, "bid_ends_at"},
		{"zero price", func() Draft { d := fixedDraft(); d.PricePaise = ptr[int64](0); return d }, "price_paise"},
		{"price above cap", func() Draft { d := fixedDraft(); d.PricePaise = ptr[int64](1_000_000_001); return d }, "price_paise"},
		{"bad category", func() Draft { d := fixedDraft(); d.Category = "cars"; return d }, "category"},
		{"six photos", func() Draft { d := fixedDraft(); d.PhotoIDs = []string{"a", "b", "c", "d", "e", "f"}; return d }, "photo_public_ids"},
		{"duplicate photo", func() Draft { d := fixedDraft(); d.PhotoIDs = []string{"a", "a"}; return d }, "photo_public_ids"},
		{"year out of range", func() Draft { d := fixedDraft(); d.Rules.AllowedYear = ptr[int16](9); return d }, "allowed_year"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Contains(t, fieldsOf(t, tc.draft().validate(now, true)), tc.field)
		})
	}

	// An untouched end time that is now close is fine when it isn't being changed.
	d := auctionDraft()
	d.BidEndsAt = ptr(now.Add(time.Minute))
	assert.NoError(t, d.validate(now, false))
}

func TestDraftApply(t *testing.T) {
	d := auctionDraft()
	assert.False(t, d.apply(Patch{Title: ptr("Acoustic guitar")}), "title change is not an auction change")
	assert.Equal(t, "Acoustic guitar", d.Title)

	d = auctionDraft()
	assert.True(t, d.apply(Patch{MinIncrementPaise: ptr[int64](10_000)}))

	d = auctionDraft()
	assert.False(t, d.apply(Patch{BidEndsAt: ptr(now.Add(24 * time.Hour))}), "same end time is not a change")

	// Switching to fixed price drops auction fields and takes the new price.
	d = auctionDraft()
	assert.True(t, d.apply(Patch{BiddingEnabled: ptr(false), PricePaise: ptr[int64](90_000)}))
	assert.Nil(t, d.StartingPricePaise)
	assert.Nil(t, d.BidEndsAt)
	assert.NoError(t, d.validate(now, false))

	// Clearing a nullable rule with an explicit null.
	d = fixedDraft()
	d.Rules.AllowedYear = ptr[int16](2)
	d.apply(Patch{AllowedYear: httpx.Optional[int16]{Set: true, Null: true}})
	assert.Nil(t, d.Rules.AllowedYear)
}
