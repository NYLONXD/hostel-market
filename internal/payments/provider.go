// Package payments collects auction payments for the platform through a pluggable
// PaymentProvider, settles them (order -> paid, ledger entry for the seller payout) and
// runs payouts. A payment is only ever marked paid by a signature-verified provider
// webhook, a provider status lookup, or an admin, never by what a client reports.
package payments

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"hostelmarket/internal/payments/upi"
)

type Status string

const (
	StatusCreated Status = "created"
	StatusPending Status = "pending"
	StatusPaid    Status = "paid"
	StatusFailed  Status = "failed"
	StatusExpired Status = "expired"
)

var (
	ErrWebhooksUnsupported = errors.New("payments: provider does not send webhooks")
	ErrStatusUnsupported   = errors.New("payments: provider can't be queried for status")
	ErrBadSignature        = errors.New("payments: webhook signature invalid")
)

// CreateRequest asks a provider to start collecting one payment.
type CreateRequest struct {
	PaymentID   uuid.UUID
	OrderID     uuid.UUID
	AmountPaise int64
	Note        string
}

// CreateResult is what the client needs to pay.
type CreateResult struct {
	ProviderRef string
	Status      Status // usually created; some gateways start at pending
	UPIURI      string
}

// WebhookEvent is a verified provider notification.
type WebhookEvent struct {
	EventID     string
	ProviderRef string
	Status      Status // paid, failed or pending
	AmountPaise int64  // 0 if the provider doesn't say
	Raw         []byte
}

// Provider is the seam for a real payment gateway (Razorpay, Cashfree, ...).
type Provider interface {
	Name() string
	CreatePayment(ctx context.Context, req CreateRequest) (CreateResult, error)
	// VerifyWebhook checks the signature and parses the event.
	VerifyWebhook(h http.Header, body []byte) (WebhookEvent, error)
	// GetStatus asks the provider for the current state of a payment.
	GetStatus(ctx context.Context, providerRef string) (Status, error)
}

// UPILinkProvider collects into the platform's own UPI ID with a upi:// intent. UPI
// intents have no callback, so payments are confirmed by an admin who checks the
// bank statement against the buyer's UTR.
type UPILinkProvider struct {
	PayeeVPA  string
	PayeeName string
}

func (p UPILinkProvider) Name() string { return "upi_link" }

func (p UPILinkProvider) CreatePayment(_ context.Context, req CreateRequest) (CreateResult, error) {
	uri, err := upi.Link{
		PayeeVPA:    p.PayeeVPA,
		PayeeName:   p.PayeeName,
		AmountPaise: req.AmountPaise,
		TxnRef:      req.PaymentID.String(),
		Note:        req.Note,
	}.URI()
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{ProviderRef: "upi_" + strings.ReplaceAll(req.PaymentID.String(), "-", ""), Status: StatusCreated, UPIURI: uri}, nil
}

func (UPILinkProvider) VerifyWebhook(http.Header, []byte) (WebhookEvent, error) {
	return WebhookEvent{}, ErrWebhooksUnsupported
}

func (UPILinkProvider) GetStatus(context.Context, string) (Status, error) {
	return "", ErrStatusUnsupported
}
