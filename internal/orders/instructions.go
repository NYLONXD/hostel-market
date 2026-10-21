package orders

import (
	"context"
	"strings"

	"hostelmarket/internal/payments/upi"
	"hostelmarket/internal/platform/httpx"
)

// PaymentInstructions tells a fixed-price buyer how to pay the seller directly.
type PaymentInstructions struct {
	Method      PaymentMethod `json:"method"`
	AmountPaise int64         `json:"amount_paise"`
	PayeeName   string        `json:"payee_name"`
	PayeeUPIID  string        `json:"payee_upi_id,omitempty"`
	UPIURI      string        `json:"upi_uri,omitempty"`
	Note        string        `json:"note"`
}

// DirectPaymentInstructions builds the buyer -> seller payment for a fixed-price order
// awaiting payment. The UPI link targets the seller's current UPI ID; the platform
// never holds this money.
func (s *Service) DirectPaymentInstructions(ctx context.Context, o Order) (*PaymentInstructions, error) {
	if o.Kind != KindFixed || o.Status != StatusAwaitingPayment || o.PaymentMethod == nil {
		return nil, nil
	}
	seller, err := s.users.Get(ctx, o.SellerID)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	pi := &PaymentInstructions{
		Method:      *o.PaymentMethod,
		AmountPaise: o.PricePaise,
		PayeeName:   seller.Name,
		Note:        "Hostel Market order " + strings.ToUpper(o.ID.String()[:8]),
	}
	if *o.PaymentMethod == MethodCash {
		pi.Note = "Pay cash to the seller at handover"
		return pi, nil
	}
	if seller.UPIID == nil {
		return nil, httpx.Conflict("seller_upi_missing", "the seller has no UPI ID on file; ask them to add one or pay cash")
	}
	pi.PayeeUPIID = *seller.UPIID
	pi.UPIURI, err = upi.Link{
		PayeeVPA: *seller.UPIID, PayeeName: seller.Name, AmountPaise: o.PricePaise,
		TxnRef: o.ID.String(), Note: pi.Note,
	}.URI()
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return pi, nil
}
