-- name: InsertListing :one
INSERT INTO listings (
  seller_id, hostel, title, description, category, condition, bidding_enabled,
  price_paise, starting_price_paise, min_increment_paise, bid_ends_at,
  available_from, available_until, allowed_block, allowed_year
) VALUES (
  sqlc.arg(seller_id), sqlc.arg(hostel), sqlc.arg(title), sqlc.arg(description), sqlc.arg(category),
  sqlc.arg(condition), sqlc.arg(bidding_enabled), sqlc.narg(price_paise), sqlc.narg(starting_price_paise),
  sqlc.narg(min_increment_paise), sqlc.narg(bid_ends_at), sqlc.narg(available_from), sqlc.narg(available_until),
  sqlc.narg(allowed_block), sqlc.narg(allowed_year)
)
RETURNING *;

-- name: GetListing :one
SELECT * FROM listings WHERE id = $1;

-- name: GetListingForUpdate :one
-- Row lock that serialises every state change of one listing (edits, bids, orders).
SELECT * FROM listings WHERE id = $1 FOR UPDATE;

-- name: UpdateListing :one
-- Full rewrite of the mutable columns; the service merges and validates first.
UPDATE listings SET
  title                = sqlc.arg(title),
  description          = sqlc.arg(description),
  category             = sqlc.arg(category),
  condition            = sqlc.arg(condition),
  bidding_enabled      = sqlc.arg(bidding_enabled),
  price_paise          = sqlc.narg(price_paise),
  starting_price_paise = sqlc.narg(starting_price_paise),
  min_increment_paise  = sqlc.narg(min_increment_paise),
  bid_ends_at          = sqlc.narg(bid_ends_at),
  auction_round        = sqlc.arg(auction_round),
  auction_closed_at    = sqlc.narg(auction_closed_at),
  first_bid_at         = sqlc.narg(first_bid_at),
  current_bid_paise    = sqlc.narg(current_bid_paise),
  bid_count            = sqlc.arg(bid_count),
  available_from       = sqlc.narg(available_from),
  available_until      = sqlc.narg(available_until),
  allowed_block        = sqlc.narg(allowed_block),
  allowed_year         = sqlc.narg(allowed_year)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetListingStatus :one
UPDATE listings SET
  status     = sqlc.arg(status)::text,
  removed_at = CASE WHEN sqlc.arg(status)::text = 'removed' THEN now() ELSE removed_at END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: InsertPhoto :exec
INSERT INTO listing_photos (listing_id, public_id, secure_url, width, height, bytes, format, position)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: DeletePhotos :exec
DELETE FROM listing_photos WHERE listing_id = $1;

-- name: ListPhotos :many
SELECT * FROM listing_photos WHERE listing_id = $1 ORDER BY position;

-- Feed queries: one per sort order so each can walk its own index with a keyset cursor.
-- Filters use the (param IS NULL OR ...) pattern; all of them are optional.

-- name: BrowseNewest :many
SELECT sqlc.embed(l),
  COALESCE((SELECT p.secure_url FROM listing_photos p WHERE p.listing_id = l.id ORDER BY p.position LIMIT 1), '')::text AS cover_url
FROM listings l
WHERE l.hostel = sqlc.arg(hostel) AND l.status = 'active'
  AND (sqlc.narg(category)::text IS NULL OR l.category = sqlc.narg(category)::text)
  AND (sqlc.narg(condition)::text IS NULL OR l.condition = sqlc.narg(condition)::text)
  AND (sqlc.narg(bidding)::boolean IS NULL OR l.bidding_enabled = sqlc.narg(bidding)::boolean)
  AND (sqlc.narg(min_price)::bigint IS NULL OR l.display_price_paise >= sqlc.narg(min_price)::bigint)
  AND (sqlc.narg(max_price)::bigint IS NULL OR l.display_price_paise <= sqlc.narg(max_price)::bigint)
  AND (sqlc.narg(query)::text IS NULL
       OR listing_search_vector(l.title, l.description) @@ websearch_to_tsquery('english', sqlc.narg(query)::text))
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (l.created_at, l.id) < (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY l.created_at DESC, l.id DESC
LIMIT sqlc.arg(row_limit);

-- name: BrowsePriceAsc :many
SELECT sqlc.embed(l),
  COALESCE((SELECT p.secure_url FROM listing_photos p WHERE p.listing_id = l.id ORDER BY p.position LIMIT 1), '')::text AS cover_url
FROM listings l
WHERE l.hostel = sqlc.arg(hostel) AND l.status = 'active'
  AND (sqlc.narg(category)::text IS NULL OR l.category = sqlc.narg(category)::text)
  AND (sqlc.narg(condition)::text IS NULL OR l.condition = sqlc.narg(condition)::text)
  AND (sqlc.narg(bidding)::boolean IS NULL OR l.bidding_enabled = sqlc.narg(bidding)::boolean)
  AND (sqlc.narg(min_price)::bigint IS NULL OR l.display_price_paise >= sqlc.narg(min_price)::bigint)
  AND (sqlc.narg(max_price)::bigint IS NULL OR l.display_price_paise <= sqlc.narg(max_price)::bigint)
  AND (sqlc.narg(query)::text IS NULL
       OR listing_search_vector(l.title, l.description) @@ websearch_to_tsquery('english', sqlc.narg(query)::text))
  AND (sqlc.narg(after_price)::bigint IS NULL
       OR (l.display_price_paise, l.id) > (sqlc.narg(after_price)::bigint, sqlc.narg(after_id)::uuid))
ORDER BY l.display_price_paise ASC, l.id ASC
LIMIT sqlc.arg(row_limit);

-- name: BrowsePriceDesc :many
SELECT sqlc.embed(l),
  COALESCE((SELECT p.secure_url FROM listing_photos p WHERE p.listing_id = l.id ORDER BY p.position LIMIT 1), '')::text AS cover_url
FROM listings l
WHERE l.hostel = sqlc.arg(hostel) AND l.status = 'active'
  AND (sqlc.narg(category)::text IS NULL OR l.category = sqlc.narg(category)::text)
  AND (sqlc.narg(condition)::text IS NULL OR l.condition = sqlc.narg(condition)::text)
  AND (sqlc.narg(bidding)::boolean IS NULL OR l.bidding_enabled = sqlc.narg(bidding)::boolean)
  AND (sqlc.narg(min_price)::bigint IS NULL OR l.display_price_paise >= sqlc.narg(min_price)::bigint)
  AND (sqlc.narg(max_price)::bigint IS NULL OR l.display_price_paise <= sqlc.narg(max_price)::bigint)
  AND (sqlc.narg(query)::text IS NULL
       OR listing_search_vector(l.title, l.description) @@ websearch_to_tsquery('english', sqlc.narg(query)::text))
  AND (sqlc.narg(after_price)::bigint IS NULL
       OR (l.display_price_paise, l.id) < (sqlc.narg(after_price)::bigint, sqlc.narg(after_id)::uuid))
ORDER BY l.display_price_paise DESC, l.id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListBySeller :many
SELECT sqlc.embed(l),
  COALESCE((SELECT p.secure_url FROM listing_photos p WHERE p.listing_id = l.id ORDER BY p.position LIMIT 1), '')::text AS cover_url
FROM listings l
WHERE l.seller_id = sqlc.arg(seller_id)
  AND (sqlc.narg(status)::text IS NULL OR l.status = sqlc.narg(status)::text)
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (l.created_at, l.id) < (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY l.created_at DESC, l.id DESC
LIMIT sqlc.arg(row_limit);
