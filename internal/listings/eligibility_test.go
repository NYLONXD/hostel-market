package listings

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/httpx"
)

func tod(t *testing.T, s string) TimeOfDay {
	t.Helper()
	v, err := ParseTimeOfDay(s)
	require.NoError(t, err)
	return v
}

func TestWindowContains(t *testing.T) {
	tests := []struct {
		name     string
		from, to string
		at       string
		want     bool
	}{
		// Same-day window 09:00-17:00.
		{"day: before start", "09:00", "17:00", "08:59", false},
		{"day: start is inclusive", "09:00", "17:00", "09:00", true},
		{"day: middle", "09:00", "17:00", "12:30", true},
		{"day: last minute", "09:00", "17:00", "16:59", true},
		{"day: end is exclusive", "09:00", "17:00", "17:00", false},
		{"day: midnight", "09:00", "17:00", "00:00", false},

		// Window crossing midnight 22:00-06:00.
		{"night: afternoon is outside", "22:00", "06:00", "14:00", false},
		{"night: just before start", "22:00", "06:00", "21:59", false},
		{"night: start is inclusive", "22:00", "06:00", "22:00", true},
		{"night: before midnight", "22:00", "06:00", "23:59", true},
		{"night: exactly midnight", "22:00", "06:00", "00:00", true},
		{"night: early morning", "22:00", "06:00", "03:15", true},
		{"night: last minute", "22:00", "06:00", "05:59", true},
		{"night: end is exclusive", "22:00", "06:00", "06:00", false},

		// Edge windows.
		{"to midnight: 23:59 inside", "18:00", "00:00", "23:59", true},
		{"to midnight: 00:00 outside", "18:00", "00:00", "00:00", false},
		{"from midnight: 00:00 inside", "00:00", "01:00", "00:00", true},
		{"almost all day wrap: 00:30 outside", "01:00", "00:30", "00:30", false},
		{"almost all day wrap: 00:29 inside", "01:00", "00:30", "00:29", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, err := NewWindow(tod(t, tc.from), tod(t, tc.to))
			require.NoError(t, err)
			assert.Equal(t, tc.want, w.Contains(tod(t, tc.at)))
		})
	}
}

func TestNewWindowRejectsEmpty(t *testing.T) {
	_, err := NewWindow(tod(t, "10:00"), tod(t, "10:00"))
	assert.Error(t, err)
}

func TestParseTimeOfDay(t *testing.T) {
	assert.Equal(t, TimeOfDay(0), tod(t, "00:00"))
	assert.Equal(t, TimeOfDay(23*60+59), tod(t, "23:59"))
	assert.Equal(t, "07:05", tod(t, "07:05").String())
	for _, bad := range []string{"24:00", "7pm", "12:60", "", "12-30"} {
		_, err := ParseTimeOfDay(bad)
		assert.Error(t, err, bad)
	}
}

func TestClockTimeUsesIST(t *testing.T) {
	// 17:00 UTC is 22:30 IST (UTC+5:30, no DST).
	assert.Equal(t, "22:30", ClockTime(time.Date(2026, 10, 21, 17, 0, 0, 0, time.UTC)).String())
	// 20:00 UTC is 01:30 IST the next day: still inside a 22:00-06:00 window.
	w, _ := NewWindow(tod(t, "22:00"), tod(t, "06:00"))
	assert.True(t, w.Contains(ClockTime(time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC))))
	// 04:00 UTC is 09:30 IST: outside.
	assert.False(t, w.Contains(ClockTime(time.Date(2026, 10, 21, 4, 0, 0, 0, time.UTC))))
}

func code(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	var apiErr *httpx.Error
	require.True(t, errors.As(err, &apiErr), "want *httpx.Error, got %v", err)
	return apiErr.Code
}

func TestCheckBuyer(t *testing.T) {
	seller := uuid.New()
	block := "B"
	year := int16(1)
	night, _ := NewWindow(tod(t, "22:00"), tod(t, "06:00"))
	l := Listing{
		SellerID: seller,
		Hostel:   "BRAHMAPUTRA",
		Status:   StatusActive,
		Rules:    Rules{Window: &night, AllowedBlock: &block, AllowedYear: &year},
	}
	ok := Buyer{ID: uuid.New(), Hostel: "BRAHMAPUTRA", Block: "B", Year: 1, Verified: true}
	at2330 := time.Date(2026, 10, 21, 23, 30, 0, 0, clock.IST)
	atNoon := time.Date(2026, 10, 21, 12, 0, 0, 0, clock.IST)

	assert.NoError(t, l.CheckBuyer(ok, at2330))

	with := func(f func(*Buyer)) Buyer { b := ok; f(&b); return b }
	tests := []struct {
		name  string
		l     Listing
		buyer Buyer
		at    time.Time
		want  string
	}{
		{"outside window", l, ok, atNoon, "outside_time_window"},
		{"wrong block", l, with(func(b *Buyer) { b.Block = "C" }), at2330, "block_restricted"},
		{"wrong year", l, with(func(b *Buyer) { b.Year = 3 }), at2330, "year_restricted"},
		{"other hostel", l, with(func(b *Buyer) { b.Hostel = "GANGA" }), at2330, "different_hostel"},
		{"unverified", l, with(func(b *Buyer) { b.Verified = false }), at2330, "not_verified"},
		{"own listing", l, with(func(b *Buyer) { b.ID = seller }), at2330, "own_listing"},
		{"sold listing", func() Listing { c := l; c.Status = StatusSold; return c }(), ok, at2330, "listing_unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, code(t, tc.l.CheckBuyer(tc.buyer, tc.at)))
		})
	}

	// Block comparison ignores case; no rules means anyone in the hostel qualifies.
	assert.NoError(t, l.CheckBuyer(with(func(b *Buyer) { b.Block = "b" }), at2330))
	open := Listing{SellerID: seller, Hostel: "BRAHMAPUTRA", Status: StatusActive}
	assert.NoError(t, open.CheckBuyer(with(func(b *Buyer) { b.Block = "Z"; b.Year = 5 }), atNoon))
}
