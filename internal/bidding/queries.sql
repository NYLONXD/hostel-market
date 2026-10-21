-- name: InsertBid :one
INSERT INTO bids (listing_id, auction_round, bidder_id, amount_paise)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: TopBid :one
SELECT * FROM bids
WHERE listing_id = $1 AND auction_round = $2
ORDER BY amount_paise DESC
LIMIT 1;

-- name: RankedBidders :many
-- Each bidder's best bid in the round, best first: the order in which the item is
-- offered when a winner fails to pay.
SELECT t.id, t.bidder_id, t.amount_paise FROM (
  SELECT DISTINCT ON (b.bidder_id) b.id, b.bidder_id, b.amount_paise
  FROM bids b
  WHERE b.listing_id = $1 AND b.auction_round = $2
  ORDER BY b.bidder_id, b.amount_paise DESC
) t
ORDER BY t.amount_paise DESC
LIMIT 50;

-- name: ListBids :many
SELECT * FROM bids
WHERE listing_id = sqlc.arg(listing_id) AND auction_round = sqlc.arg(auction_round)
  AND (sqlc.narg(before_id)::bigint IS NULL OR id < sqlc.narg(before_id)::bigint)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);
