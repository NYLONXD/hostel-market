package payments

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hostelmarket/internal/orders"
	"hostelmarket/internal/payments/store"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/httpx"
)

// Payout is a ledger entry: what the platform collected for an auction order and owes
// the seller.
type Payout struct {
	ID                uuid.UUID  `json:"id"`
	OrderID           uuid.UUID  `json:"order_id"`
	PaymentID         uuid.UUID  `json:"payment_id"`
	SellerID          uuid.UUID  `json:"seller_id"`
	GrossPaise        int64      `json:"gross_paise"`
	PlatformFeePaise  int64      `json:"platform_fee_paise"`
	SellerPayoutPaise int64      `json:"seller_payout_paise"`
	PayoutStatus      string     `json:"payout_status"`
	PayoutReference   *string    `json:"payout_reference,omitempty"`
	PaidOutAt         *time.Time `json:"paid_out_at,omitempty"`
	PaidOutBy         *uuid.UUID `json:"paid_out_by,omitempty"`
	OrderStatus       string     `json:"order_status"`
	// Releasable: the buyer has confirmed receipt, so the payout may be sent.
	Releasable bool      `json:"releasable"`
	CreatedAt  time.Time `json:"created_at"`
}

type PayoutCursor struct {
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"i"`
}

func (s *Service) ListPayouts(ctx context.Context, status *string, after *PayoutCursor, limit int32) ([]Payout, *PayoutCursor, error) {
	p := store.ListLedgerParams{PayoutStatus: status, RowLimit: limit + 1}
	if after != nil {
		p.AfterCreated, p.AfterID = &after.CreatedAt, &after.ID
	}
	rows, err := store.New(s.db.Reader(ctx, db.Strong)).ListLedger(ctx, p)
	if err != nil {
		return nil, nil, httpx.Internal(err)
	}
	out := make([]Payout, 0, len(rows))
	for _, r := range rows {
		out = append(out, toPayout(r.LedgerEntry, r.OrderStatus))
	}
	var next *PayoutCursor
	if len(out) > int(limit) {
		out = out[:limit]
		last := out[len(out)-1]
		next = &PayoutCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return out, next, nil
}

// MarkPayoutPaid records that the seller was paid (e.g. a bank/UPI transfer reference).
// Payouts are released only after the buyer confirmed receipt (order completed).
func (s *Service) MarkPayoutPaid(ctx context.Context, adminID, ledgerID uuid.UUID, reference string) (Payout, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" || len(reference) > 100 {
		return Payout{}, httpx.ValidationFailed(httpx.FieldError{Field: "reference", Rule: "len", Message: "must be 1-100 characters"})
	}
	var out Payout
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.GetLedgerEntryForUpdate(ctx, ledgerID)
		if db.IsNoRows(err) {
			return httpx.NotFound("payout not found")
		}
		if err != nil {
			return err
		}
		switch {
		case row.LedgerEntry.PayoutStatus == "paid_out":
			return httpx.Conflict("already_paid_out", "this payout was already marked paid")
		case row.OrderStatus != string(orders.StatusCompleted):
			return httpx.Conflict("order_not_completed", "payouts are released after the buyer confirms receipt (order is "+row.OrderStatus+")")
		}
		updated, err := q.MarkPayoutPaid(ctx, store.MarkPayoutPaidParams{ID: ledgerID, Reference: reference, AdminID: adminID})
		if err != nil {
			return err
		}
		out = toPayout(updated, row.OrderStatus)
		return nil
	})
	if err != nil {
		return Payout{}, asAPIError(err)
	}
	if s.notify != nil {
		s.notify.PublishToUsers(ctx, []uuid.UUID{out.SellerID}, "payout.paid", map[string]any{
			"order_id": out.OrderID, "amount_paise": out.SellerPayoutPaise, "reference": reference,
		})
	}
	return out, nil
}

func toPayout(l store.LedgerEntry, orderStatus string) Payout {
	return Payout{
		ID: l.ID, OrderID: l.OrderID, PaymentID: l.PaymentID, SellerID: l.SellerID, GrossPaise: l.GrossPaise,
		PlatformFeePaise: l.PlatformFeePaise, SellerPayoutPaise: l.SellerPayoutPaise, PayoutStatus: l.PayoutStatus,
		PayoutReference: l.PayoutReference, PaidOutAt: l.PaidOutAt, PaidOutBy: l.PaidOutBy, OrderStatus: orderStatus,
		Releasable: l.PayoutStatus == "pending" && orderStatus == string(orders.StatusCompleted), CreatedAt: l.CreatedAt,
	}
}
