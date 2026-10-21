package money

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitFee(t *testing.T) {
	tests := []struct {
		name       string
		price      int64
		bps        int64
		wantFee    int64
		wantPayout int64
	}{
		{"20% of round amount", 100_000, 2000, 20_000, 80_000},
		{"20% of odd amount rounds down below half", 12_345, 2000, 2_469, 9_876}, // 2469.0
		{"20% of 1 paise is 0.2 -> 0", 1, 2000, 0, 1},
		{"20% of 3 paise is 0.6 -> 1", 3, 2000, 1, 2},
		{"20% of 99999 is 19999.8 -> 20000", 99_999, 2000, 20_000, 79_999},
		{"10% of 5 paise is exactly 0.5 -> rounds up", 5, 1000, 1, 4},
		{"10% of 15 paise is exactly 1.5 -> rounds up", 15, 1000, 2, 13},
		{"2% of 25 paise is exactly 0.5 -> rounds up", 25, 200, 1, 24},
		{"12.5% of 1001 is 125.125 -> 125", 1001, 1250, 125, 876},
		{"12.5% of 1004 is 125.5 -> 126", 1004, 1250, 126, 878},
		{"0.01% of 4999 is 0.4999 -> 0", 4999, 1, 0, 4999},
		{"0.01% of 5000 is exactly 0.5 -> 1", 5000, 1, 1, 4999},
		{"0% fee", 77_777, 0, 0, 77_777},
		{"100% fee", 77_777, 10_000, 77_777, 0},
		{"max price at 20%", MaxPaise, 2000, 200_000_000, 800_000_000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fee, payout, err := SplitFee(tc.price, tc.bps)
			require.NoError(t, err)
			assert.Equal(t, tc.wantFee, fee, "fee")
			assert.Equal(t, tc.wantPayout, payout, "payout")
			assert.Equal(t, tc.price, fee+payout, "fee + payout must equal price")
		})
	}
}

func TestSplitFeeRejectsBadInput(t *testing.T) {
	tests := []struct {
		name  string
		price int64
		bps   int64
	}{
		{"zero price", 0, 2000},
		{"negative price", -100, 2000},
		{"price above cap", MaxPaise + 1, 2000},
		{"negative bps", 100, -1},
		{"bps above 100%", 100, MaxBps + 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := SplitFee(tc.price, tc.bps)
			assert.Error(t, err)
		})
	}
}

func TestParsePercent(t *testing.T) {
	valid := map[string]int64{
		"20":    2000,
		"0":     0,
		"100":   10_000,
		"12.5":  1250,
		"12.50": 1250,
		"0.01":  1,
		" 7 ":   700,
		"99.99": 9999,
	}
	for in, want := range valid {
		got, err := ParsePercent(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}

	for _, in := range []string{"", "abc", "-5", "100.01", "101", "12.345", "12.", ".5", "1e2", "20%"} {
		_, err := ParsePercent(in)
		assert.Error(t, err, "input %q", in)
	}
}

func TestRupees(t *testing.T) {
	assert.Equal(t, "0.00", Rupees(0))
	assert.Equal(t, "0.05", Rupees(5))
	assert.Equal(t, "1.00", Rupees(100))
	assert.Equal(t, "1234.50", Rupees(123_450))
	assert.Equal(t, "-2.01", Rupees(-201))
}
