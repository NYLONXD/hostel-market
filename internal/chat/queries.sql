-- name: UpsertConversation :one
-- Idempotent "open chat": returns the existing conversation for (listing, buyer).
INSERT INTO conversations (listing_id, buyer_id, seller_id)
VALUES ($1, $2, $3)
ON CONFLICT (listing_id, buyer_id) DO UPDATE SET listing_id = EXCLUDED.listing_id
RETURNING *;

-- name: GetConversation :one
SELECT * FROM conversations WHERE id = $1;

-- name: GetConversationForUpdate :one
SELECT * FROM conversations WHERE id = $1 FOR UPDATE;

-- name: InsertMessage :one
INSERT INTO messages (conversation_id, sender_id, client_msg_id, body)
VALUES ($1, $2, $3, $4)
ON CONFLICT (conversation_id, sender_id, client_msg_id) DO NOTHING
RETURNING *;

-- name: GetMessageByClientID :one
SELECT * FROM messages WHERE conversation_id = $1 AND sender_id = $2 AND client_msg_id = $3;

-- name: TouchConversation :exec
-- After a message: bump recency and mark it read for its sender.
UPDATE conversations SET
  last_message_at     = sqlc.arg(at),
  buyer_last_read_id  = CASE WHEN buyer_id  = sqlc.arg(sender_id) THEN GREATEST(buyer_last_read_id,  sqlc.arg(message_id)) ELSE buyer_last_read_id END,
  seller_last_read_id = CASE WHEN seller_id = sqlc.arg(sender_id) THEN GREATEST(seller_last_read_id, sqlc.arg(message_id)) ELSE seller_last_read_id END
WHERE id = sqlc.arg(id);

-- name: MarkRead :one
-- Read pointers only move forward and never past the newest message.
UPDATE conversations c SET
  buyer_last_read_id  = CASE WHEN c.buyer_id  = sqlc.arg(user_id)
                             THEN GREATEST(c.buyer_last_read_id,  LEAST(sqlc.arg(up_to)::bigint, m.max_id)) ELSE c.buyer_last_read_id END,
  seller_last_read_id = CASE WHEN c.seller_id = sqlc.arg(user_id)
                             THEN GREATEST(c.seller_last_read_id, LEAST(sqlc.arg(up_to)::bigint, m.max_id)) ELSE c.seller_last_read_id END
FROM (SELECT COALESCE(max(id), 0)::bigint AS max_id FROM messages WHERE conversation_id = sqlc.arg(id)) m
WHERE c.id = sqlc.arg(id)
RETURNING c.*;

-- name: ListMessagesBefore :many
SELECT * FROM messages
WHERE conversation_id = sqlc.arg(conversation_id)
  AND (sqlc.narg(before_id)::bigint IS NULL OR id < sqlc.narg(before_id)::bigint)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListMessagesAfter :many
SELECT * FROM messages
WHERE conversation_id = sqlc.arg(conversation_id) AND id > sqlc.arg(after_id)
ORDER BY id ASC
LIMIT sqlc.arg(row_limit);

-- name: ListConversations :many
SELECT sqlc.embed(c),
  (SELECT count(*) FROM messages m
    WHERE m.conversation_id = c.id AND m.sender_id <> sqlc.arg(user_id)
      AND m.id > CASE WHEN c.buyer_id = sqlc.arg(user_id) THEN c.buyer_last_read_id ELSE c.seller_last_read_id END
  )::bigint AS unread,
  COALESCE((SELECT m.body FROM messages m WHERE m.conversation_id = c.id ORDER BY m.id DESC LIMIT 1), '')::text AS last_body
FROM conversations c
WHERE (c.buyer_id = sqlc.arg(user_id) OR c.seller_id = sqlc.arg(user_id))
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (COALESCE(c.last_message_at, c.created_at), c.id) < (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY COALESCE(c.last_message_at, c.created_at) DESC, c.id DESC
LIMIT sqlc.arg(row_limit);

-- name: CountUnread :one
SELECT COALESCE(count(*), 0)::bigint FROM messages m
JOIN conversations c ON c.id = m.conversation_id
WHERE (c.buyer_id = sqlc.arg(user_id) OR c.seller_id = sqlc.arg(user_id))
  AND m.sender_id <> sqlc.arg(user_id)
  AND m.id > CASE WHEN c.buyer_id = sqlc.arg(user_id) THEN c.buyer_last_read_id ELSE c.seller_last_read_id END;
