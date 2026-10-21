-- name: InsertOrder :one
INSERT INTO orders (
  listing_id, buyer_id, seller_id, kind, status, price_paise,
  platform_fee_bps, platform_fee_paise, seller_payout_paise, payment_method,
  auction_round, winning_bid_id, buyer_note, request_expires_at, payment_due_at
) VALUES (
  sqlc.arg(listing_id), sqlc.arg(buyer_id), sqlc.arg(seller_id), sqlc.arg(kind), sqlc.arg(status), sqlc.arg(price_paise),
  sqlc.arg(platform_fee_bps), sqlc.arg(platform_fee_paise), sqlc.arg(seller_payout_paise), sqlc.narg(payment_method),
  sqlc.narg(auction_round), sqlc.narg(winning_bid_id), sqlc.narg(buyer_note), sqlc.narg(request_expires_at),
  sqlc.narg(payment_due_at)
)
RETURNING *;

-- name: GetOrder :one
SELECT * FROM orders WHERE id = $1;

-- name: GetOrderForUpdate :one
SELECT * FROM orders WHERE id = $1 FOR UPDATE;

-- name: TransitionOrder :one
-- Moves an order to a new status and stamps the matching timestamp. The WHERE clause
-- re-checks the expected current status so a lost race updates nothing.
UPDATE orders SET
  status         = sqlc.arg(to_status)::text,
  payment_method = COALESCE(sqlc.narg(payment_method)::text, payment_method),
  accepted_at    = CASE WHEN sqlc.arg(to_status)::text = 'accepted'    THEN now() ELSE accepted_at END,
  paid_at        = CASE WHEN sqlc.arg(to_status)::text = 'paid'        THEN now() ELSE paid_at END,
  handed_over_at = CASE WHEN sqlc.arg(to_status)::text = 'handed_over' THEN now() ELSE handed_over_at END,
  completed_at   = CASE WHEN sqlc.arg(to_status)::text = 'completed'   THEN now() ELSE completed_at END,
  closed_at      = CASE WHEN sqlc.arg(to_status)::text IN ('rejected', 'cancelled', 'expired') THEN now() ELSE closed_at END,
  close_reason   = COALESCE(sqlc.narg(close_reason)::text, close_reason)
WHERE id = sqlc.arg(id) AND status = sqlc.arg(from_status)::text
RETURNING *;

-- name: RejectOpenRequests :many
UPDATE orders SET status = 'rejected', closed_at = now(), close_reason = sqlc.arg(reason)::text
WHERE listing_id = sqlc.arg(listing_id) AND status = 'requested'
RETURNING id;

-- name: GetLiveOrderForListing :one
SELECT * FROM orders
WHERE listing_id = $1 AND status IN ('accepted', 'awaiting_payment', 'paid', 'handed_over', 'completed');

-- name: ListOrdersForUser :many
SELECT * FROM orders
WHERE ((sqlc.arg(as_buyer)::boolean AND buyer_id = sqlc.arg(user_id))
    OR (sqlc.arg(as_seller)::boolean AND seller_id = sqlc.arg(user_id)))
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListOverdueRequests :many
SELECT id FROM orders
WHERE status = 'requested' AND request_expires_at <= now()
ORDER BY request_expires_at
LIMIT 200;
