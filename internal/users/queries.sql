-- name: CreateUser :one
INSERT INTO users (email, password_hash, name, hostel, block, year, upi_id, is_verified, verified_at)
VALUES (
  sqlc.arg(email), sqlc.arg(password_hash), sqlc.arg(name), sqlc.arg(hostel), sqlc.arg(block),
  sqlc.arg(year), sqlc.narg(upi_id), sqlc.arg(is_verified)::boolean,
  CASE WHEN sqlc.arg(is_verified)::boolean THEN now() END
)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: UpdateProfile :one
-- NULL leaves a field unchanged; set_upi_id distinguishes "clear UPI ID" from "leave it".
UPDATE users SET
  name   = COALESCE(sqlc.narg(name), name),
  block  = COALESCE(sqlc.narg(block), block),
  year   = COALESCE(sqlc.narg(year), year),
  upi_id = CASE WHEN sqlc.arg(set_upi_id)::boolean THEN sqlc.narg(upi_id) ELSE upi_id END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetVerified :one
UPDATE users SET
  is_verified = sqlc.arg(verified)::boolean,
  verified_at = CASE WHEN sqlc.arg(verified)::boolean THEN now() END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ListUnverifiedUsers :many
SELECT * FROM users
WHERE NOT is_verified
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (created_at, id) > (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY created_at, id
LIMIT sqlc.arg(row_limit);

-- name: IncrementPaymentDefaults :one
UPDATE users SET payment_defaults = payment_defaults + 1 WHERE id = $1 RETURNING payment_defaults;

-- name: ResetPaymentDefaults :one
UPDATE users SET payment_defaults = 0 WHERE id = $1 RETURNING *;
