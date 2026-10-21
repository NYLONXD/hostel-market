// Package upi builds UPI deep links (upi://pay?...). The mobile client opens the link
// and the OS offers the installed UPI apps (GPay, PhonePe, Paytm, ...); the backend
// only produces the payload and never detects apps itself.
package upi

import (
	"errors"
	"net/url"
	"strings"
	"unicode"

	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/money"
)

const (
	maxRefLen  = 35 // tr: NPCI transaction reference limit
	maxNoteLen = 50 // tn: apps truncate or reject longer notes
	maxNameLen = 50
)

// Link is one payment request.
type Link struct {
	PayeeVPA    string // pa
	PayeeName   string // pn
	AmountPaise int64  // am, rendered in rupees
	TxnRef      string // tr, alphanumeric
	Note        string // tn
}

// URI renders the link. Values are percent-encoded with %20 for spaces: several UPI
// apps mis-handle '+'.
func (l Link) URI() (string, error) {
	if !httpx.ValidUPIID(l.PayeeVPA) {
		return "", errors.New("upi: invalid payee VPA")
	}
	if l.AmountPaise <= 0 || l.AmountPaise > money.MaxPaise {
		return "", errors.New("upi: amount out of range")
	}
	ref := alnum(l.TxnRef, maxRefLen)
	if ref == "" {
		return "", errors.New("upi: transaction reference is required")
	}
	params := []struct{ k, v string }{
		{"pa", l.PayeeVPA},
		{"pn", truncate(l.PayeeName, maxNameLen)},
		{"am", money.Rupees(l.AmountPaise)},
		{"tr", ref},
		{"tn", truncate(l.Note, maxNoteLen)},
		{"cu", "INR"},
	}
	var b strings.Builder
	b.WriteString("upi://pay?")
	for i, p := range params {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(strings.ReplaceAll(url.QueryEscape(p.v), "+", "%20"))
	}
	return b.String(), nil
}

func alnum(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			if b.Len() == limit {
				break
			}
		}
	}
	return b.String()
}

func truncate(s string, limit int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > limit {
		return string(r[:limit])
	}
	return s
}
