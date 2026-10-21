// Package money does integer arithmetic on paise. Floats never touch money.
package money

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	// MaxBps is 100% expressed in basis points (1% = 100 bps).
	MaxBps int64 = 10_000
	// MaxPaise caps any single amount at ₹1 crore, which keeps price*bps far inside int64.
	MaxPaise int64 = 1_000_000_000
)

var ErrAmountOutOfRange = errors.New("money: amount out of range")

// ParsePercent converts a decimal percent such as "20", "12.5" or "0.25" into basis points.
// At most two decimal places are accepted so the conversion is exact.
func ParsePercent(s string) (int64, error) {
	s = strings.TrimSpace(s)
	whole, frac, hasDot := strings.Cut(s, ".")
	if !isDigits(whole) || (hasDot && !isDigits(frac)) || len(frac) > 2 {
		return 0, fmt.Errorf("money: invalid percent %q (want e.g. 20 or 12.5)", s)
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w > 100 {
		return 0, fmt.Errorf("money: percent %q out of range 0-100", s)
	}
	var f int64
	if frac != "" {
		f, _ = strconv.ParseInt(frac+strings.Repeat("0", 2-len(frac)), 10, 64)
	}
	bps := w*100 + f
	if bps > MaxBps {
		return 0, fmt.Errorf("money: percent %q out of range 0-100", s)
	}
	return bps, nil
}

// SplitFee returns the platform fee and the seller payout for a price.
// fee = round-half-up(price * bps / 10000), payout = price - fee.
func SplitFee(pricePaise, feeBps int64) (feePaise, payoutPaise int64, err error) {
	if pricePaise <= 0 || pricePaise > MaxPaise {
		return 0, 0, fmt.Errorf("%w: price %d", ErrAmountOutOfRange, pricePaise)
	}
	if feeBps < 0 || feeBps > MaxBps {
		return 0, 0, fmt.Errorf("money: fee %d bps out of range", feeBps)
	}
	feePaise = (pricePaise*feeBps + MaxBps/2) / MaxBps
	return feePaise, pricePaise - feePaise, nil
}

// Rupees formats paise as a plain decimal rupee string ("1234.50"), the form UPI's am= expects.
func Rupees(paise int64) string {
	sign := ""
	if paise < 0 {
		sign, paise = "-", -paise
	}
	return fmt.Sprintf("%s%d.%02d", sign, paise/100, paise%100)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
