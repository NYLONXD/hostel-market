"""Buy requests and the order life cycle. The rules live in PostgreSQL
(fn_request_order / fn_transition_order); this module only calls them."""

from typing import Literal

from fastapi import APIRouter, Body

from .. import db
from ..config import get_settings
from ..deps import CurrentUser
from ..errors import not_found
from ..schemas import OrderAction, OrderActionIn, OrderRequestIn
from .listings import bump_listing_feed

router = APIRouter(tags=["orders"])

# Actions that change the listing's status (and therefore the feed).
_FEED_ACTIONS = {"accept", "cancel", "confirm_payment"}


def order_for(order_id: int, user: dict) -> dict:
    row = db.fetch_one("SELECT * FROM v_order_details WHERE order_id = %s", (order_id,))
    if row is None or (user["user_id"] not in (row["buyer_id"], row["seller_id"]) and user["role"] != "admin"):
        raise not_found("order not found")
    row["my_role"] = "buyer" if row["buyer_id"] == user["user_id"] else "seller" if row["seller_id"] == user["user_id"] else "admin"
    row["history"] = db.fetch_all(
        """
        SELECT h.history_id, h.old_status, h.new_status, h.changed_by, u.full_name AS changed_by_name, h.note, h.changed_at
        FROM order_status_history h
        LEFT JOIN users u ON u.user_id = h.changed_by
        WHERE h.order_id = %s
        ORDER BY h.history_id
        """,
        (order_id,),
    )
    return row


@router.post("/listings/{listing_id}/orders", status_code=201)
def request_order(listing_id: int, user: CurrentUser, body: OrderRequestIn | None = Body(None)) -> dict:
    row = db.fetch_one(
        "SELECT order_id FROM fn_request_order(%s, %s, %s, make_interval(hours => %s))",
        (listing_id, user["user_id"], body.note if body else None, get_settings().order_request_ttl_hours),
    )
    return order_for(row["order_id"], user)


@router.get("/orders")
def list_orders(
    user: CurrentUser,
    role: Literal["buying", "selling", "all"] = "all",
    status: str | None = None,
    listing_id: int | None = None,
) -> list[dict]:
    return db.fetch_all(
        """
        SELECT *, CASE WHEN buyer_id = %(me)s THEN 'buyer' ELSE 'seller' END AS my_role
        FROM v_order_details
        WHERE ((%(role)s IN ('buying', 'all') AND buyer_id = %(me)s)
            OR (%(role)s IN ('selling', 'all') AND seller_id = %(me)s))
          AND (%(status)s::text IS NULL OR status = %(status)s::text)
          AND (%(listing)s::bigint IS NULL OR listing_id = %(listing)s::bigint)
        ORDER BY created_at DESC
        LIMIT 200
        """,
        {"me": user["user_id"], "role": role, "status": status, "listing": listing_id},
    )


@router.get("/orders/{order_id}")
def get_order(order_id: int, user: CurrentUser) -> dict:
    return order_for(order_id, user)


@router.post("/orders/{order_id}/{action}")
def act_on_order(order_id: int, action: OrderAction, user: CurrentUser, body: OrderActionIn | None = Body(None)) -> dict:
    body = body or OrderActionIn()
    row = db.fetch_one(
        "SELECT order_id, listing_id FROM fn_transition_order(%s, %s, %s, %s, %s)",
        (order_id, user["user_id"], action, body.payment_method, body.reason),
    )
    if action in _FEED_ACTIONS:
        bump_listing_feed(row["listing_id"])
    return order_for(order_id, user)
