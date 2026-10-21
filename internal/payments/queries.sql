-- name: InsertPayment :one
INSERT INTO payments (order_id, payer_id, provider, amount_paise, status, idempotency_key, expires_at)
VALUES ($1, $2, $3, $4, 'created', $5, $6)
RETURNING *;

-- name: GetPayment :one
SELECT * FROM payments WHERE id = $1;

-- name: GetPaymentForUpdate :one
SELECT * FROM payments WHERE id = $1 FOR UPDATE;

-- name: GetPaymentByIdempotencyKey :one
SELECT * FROM payments WHERE payer_id = $1 AND idempotency_key = $2;

-- name: GetLivePaymentForOrder :one
SELECT * FROM payments WHERE order_id = $1 AND status IN ('created', 'pending');

-- name: GetPaymentByProviderRef :one
SELECT * FROM payments WHERE provider = $1 AND provider_ref = $2;

-- name: SetProviderResult :one
UPDATE payments SET provider_ref = $2, upi_uri = $3, status = $4 WHERE id = $1 RETURNING *;

-- name: SetPaymentStatus :one
UPDATE payments SET
  status         = sqlc.arg(status)::text,
  failure_reason = COALESCE(sqlc.narg(failure_reason)::text, failure_reason),
  verified_by    = COALESCE(sqlc.narg(verified_by)::uuid, verified_by),
  paid_at        = CASE WHEN sqlc.arg(status)::text = 'paid' THEN now() ELSE paid_at END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetBuyerUTR :one
-- A buyer-reported reference only moves created -> pending; it never marks a payment paid.
UPDATE payments SET
  buyer_utr = sqlc.arg(utr)::text,
  status    = CASE WHEN status = 'created' THEN 'pending' ELSE status END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ExpireOpenPayments :exec
UPDATE payments SET status = 'expired' WHERE order_id = $1 AND status IN ('created', 'pending');

-- name: ListPaymentsForOrder :many
SELECT * FROM payments WHERE order_id = $1 ORDER BY created_at DESC;

-- name: ListPaymentsByStatus :many
SELECT * FROM payments
WHERE status = sqlc.arg(status)::text
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (created_at, id) > (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY created_at, id
LIMIT sqlc.arg(row_limit);

-- name: InsertWebhookEvent :one
-- Replays hit the unique (provider, event_id) key and return no row.
INSERT INTO payment_webhook_events (provider, event_id, payload)
VALUES ($1, $2, $3)
ON CONFLICT (provider, event_id) DO NOTHING
RETURNING id;

-- name: MarkWebhookProcessed :exec
UPDATE payment_webhook_events SET processed_at = now() WHERE id = $1;

-- name: InsertLedgerEntry :one
INSERT INTO ledger_entries (order_id, payment_id, seller_id, gross_paise, platform_fee_paise, seller_payout_paise)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListLedger :many
SELECT sqlc.embed(l),
  (SELECT o.status FROM orders o WHERE o.id = l.order_id)::text AS order_status
FROM ledger_entries l
WHERE (sqlc.narg(payout_status)::text IS NULL OR l.payout_status = sqlc.narg(payout_status)::text)
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (l.created_at, l.id) > (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY l.created_at, l.id
LIMIT sqlc.arg(row_limit);

-- name: GetLedgerEntryForUpdate :one
SELECT sqlc.embed(l),
  (SELECT o.status FROM orders o WHERE o.id = l.order_id)::text AS order_status
FROM ledger_entries l
WHERE l.id = $1
FOR UPDATE OF l;

-- name: MarkPayoutPaid :one
UPDATE ledger_entries SET
  payout_status    = 'paid_out',
  payout_reference = sqlc.arg(reference)::text,
  paid_out_at      = now(),
  paid_out_by      = sqlc.arg(admin_id)::uuid
WHERE id = sqlc.arg(id) AND payout_status = 'pending'
RETURNING *;
