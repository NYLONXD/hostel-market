-- name: InsertRefreshToken :exec
INSERT INTO refresh_tokens (jti, user_id, family_id, expires_at) VALUES ($1, $2, $3, $4);

-- name: GetRefreshTokenForUpdate :one
SELECT * FROM refresh_tokens WHERE jti = $1 FOR UPDATE;

-- name: MarkRefreshTokenUsed :exec
UPDATE refresh_tokens SET used_at = now() WHERE jti = $1;

-- name: RevokeRefreshFamily :exec
UPDATE refresh_tokens SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL;

-- name: DeleteExpiredRefreshTokens :execrows
DELETE FROM refresh_tokens WHERE expires_at < now() - interval '1 day';
