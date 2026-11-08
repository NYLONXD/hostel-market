"""Buyer-seller chat. Plain REST: the web app polls for new messages with after_id."""

from fastapi import APIRouter, Query

from .. import db
from ..deps import CurrentUser
from ..errors import not_found
from ..schemas import MessageIn, ReadIn

router = APIRouter(tags=["chat"])

_CONVERSATION_SQL = """
SELECT c.conversation_id, c.listing_id, l.title AS listing_title, l.status AS listing_status,
       (SELECT p.url FROM listing_photos p WHERE p.listing_id = l.listing_id ORDER BY p.position LIMIT 1) AS listing_cover_url,
       c.buyer_id, bu.full_name AS buyer_name, l.seller_id, su.full_name AS seller_name,
       CASE WHEN c.buyer_id = %(me)s THEN su.full_name ELSE bu.full_name END AS other_name,
       CASE WHEN c.buyer_id = %(me)s THEN c.buyer_last_read_id ELSE c.seller_last_read_id END AS my_last_read_id,
       CASE WHEN c.buyer_id = %(me)s THEN c.seller_last_read_id ELSE c.buyer_last_read_id END AS other_last_read_id,
       (SELECT count(*) FROM messages m
         WHERE m.conversation_id = c.conversation_id AND m.sender_id <> %(me)s
           AND m.message_id > CASE WHEN c.buyer_id = %(me)s THEN c.buyer_last_read_id ELSE c.seller_last_read_id END
       ) AS unread,
       (SELECT m.body FROM messages m WHERE m.conversation_id = c.conversation_id
         ORDER BY m.message_id DESC LIMIT 1) AS last_message,
       c.last_message_at, c.created_at
FROM conversations c
JOIN listings l  ON l.listing_id = c.listing_id
JOIN users    bu ON bu.user_id   = c.buyer_id
JOIN users    su ON su.user_id   = l.seller_id
"""


def conversation_for(conversation_id: int, user: dict) -> dict:
    row = db.fetch_one(
        _CONVERSATION_SQL + " WHERE c.conversation_id = %(id)s AND (c.buyer_id = %(me)s OR l.seller_id = %(me)s)",
        {"id": conversation_id, "me": user["user_id"]},
    )
    if row is None:
        raise not_found("conversation not found")
    return row


@router.post("/listings/{listing_id}/conversations")
def open_conversation(listing_id: int, user: CurrentUser) -> dict:
    row = db.fetch_one("SELECT conversation_id FROM fn_open_conversation(%s, %s)", (listing_id, user["user_id"]))
    return conversation_for(row["conversation_id"], user)


@router.get("/conversations")
def list_conversations(user: CurrentUser) -> list[dict]:
    return db.fetch_all(
        _CONVERSATION_SQL
        + " WHERE c.buyer_id = %(me)s OR l.seller_id = %(me)s ORDER BY COALESCE(c.last_message_at, c.created_at) DESC",
        {"me": user["user_id"]},
    )


@router.get("/conversations/unread-count")
def unread_count(user: CurrentUser) -> dict:
    row = db.fetch_one(
        """
        SELECT count(*) AS unread
        FROM messages m
        JOIN conversations c ON c.conversation_id = m.conversation_id
        JOIN listings l      ON l.listing_id = c.listing_id
        WHERE (c.buyer_id = %(me)s OR l.seller_id = %(me)s)
          AND m.sender_id <> %(me)s
          AND m.message_id > CASE WHEN c.buyer_id = %(me)s THEN c.buyer_last_read_id ELSE c.seller_last_read_id END
        """,
        {"me": user["user_id"]},
    )
    return {"unread": row["unread"]}


@router.get("/conversations/{conversation_id}/messages")
def messages(
    conversation_id: int,
    user: CurrentUser,
    after_id: int | None = Query(None, ge=0, description="only messages newer than this id (polling)"),
    before_id: int | None = Query(None, ge=1, description="older page ending before this id"),
    limit: int = Query(50, ge=1, le=100),
) -> dict:
    conv = conversation_for(conversation_id, user)
    if after_id is not None:
        rows = db.fetch_all(
            """SELECT message_id, sender_id, body, created_at FROM messages
               WHERE conversation_id = %s AND message_id > %s ORDER BY message_id LIMIT %s""",
            (conversation_id, after_id, limit),
        )
    else:
        rows = db.fetch_all(
            """SELECT * FROM (
                 SELECT message_id, sender_id, body, created_at FROM messages
                 WHERE conversation_id = %s AND (%s::bigint IS NULL OR message_id < %s::bigint)
                 ORDER BY message_id DESC LIMIT %s
               ) newest ORDER BY message_id""",
            (conversation_id, before_id, before_id, limit),
        )
    return {"conversation": conv, "messages": rows}


@router.post("/conversations/{conversation_id}/messages", status_code=201)
def send_message(conversation_id: int, body: MessageIn, user: CurrentUser) -> dict:
    conversation_for(conversation_id, user)  # 404 for strangers
    # The messages triggers re-check the sender and update the conversation.
    return db.fetch_one(
        "INSERT INTO messages (conversation_id, sender_id, body) VALUES (%s, %s, %s) "
        "RETURNING message_id, sender_id, body, created_at",
        (conversation_id, user["user_id"], body.body),
    )


@router.post("/conversations/{conversation_id}/read")
def mark_read(conversation_id: int, body: ReadIn, user: CurrentUser) -> dict:
    conversation_for(conversation_id, user)
    # Read pointers only move forward and never past the newest message.
    db.execute(
        """
        UPDATE conversations c SET
          buyer_last_read_id  = CASE WHEN c.buyer_id =  %(me)s THEN GREATEST(c.buyer_last_read_id,  LEAST(%(up_to)s, m.max_id)) ELSE c.buyer_last_read_id END,
          seller_last_read_id = CASE WHEN c.buyer_id <> %(me)s THEN GREATEST(c.seller_last_read_id, LEAST(%(up_to)s, m.max_id)) ELSE c.seller_last_read_id END
        FROM (SELECT COALESCE(max(message_id), 0) AS max_id FROM messages WHERE conversation_id = %(id)s) m
        WHERE c.conversation_id = %(id)s
        """,
        {"me": user["user_id"], "up_to": body.up_to_id, "id": conversation_id},
    )
    return conversation_for(conversation_id, user)
