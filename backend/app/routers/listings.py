from typing import Literal

from fastapi import APIRouter, Query, UploadFile
from psycopg import sql

from .. import cache, db, storage
from ..config import get_settings
from ..deps import CurrentUser
from ..errors import AppError, conflict, forbidden, invalid, not_found
from ..schemas import Condition, ListingIn, ListingPatch

router = APIRouter(tags=["listings"])

# Columns of v_listing_cards returned by the API (search_doc is internal).
LISTING_COLS = """
  listing_id, title, description, price, item_condition AS condition, status,
  category, category_name, seller_id, seller_name, seller_block, hostel_id, hostel_name,
  available_from, available_until, allowed_block_id, allowed_block, allowed_year,
  cover_url, photo_count, created_at, updated_at
"""

_ORDER_BY = {
    "newest": "created_at DESC, listing_id DESC",
    "price_asc": "price ASC, listing_id ASC",
    "price_desc": "price DESC, listing_id DESC",
}


def get_card(listing_id: int) -> dict | None:
    return db.fetch_one(f"SELECT {LISTING_COLS} FROM v_listing_cards WHERE listing_id = %s", (listing_id,))


def bump_listing_feed(listing_id: int) -> None:
    """Invalidate the cached feed of the hostel this listing belongs to."""
    if not cache.enabled():
        return
    row = db.fetch_one("SELECT fn_user_hostel(seller_id) AS hostel_id FROM listings WHERE listing_id = %s", (listing_id,))
    if row:
        cache.bump_feed(row["hostel_id"])


def _category_id(slug: str) -> int:
    row = db.fetch_one("SELECT category_id FROM categories WHERE slug = %s", (slug,))
    if row is None:
        raise invalid("category", "unknown category")
    return row["category_id"]


def _lock_own_listing(conn, listing_id: int, user: dict) -> dict:
    row = db.fetch_one("SELECT seller_id, status FROM listings WHERE listing_id = %s FOR UPDATE", (listing_id,), conn=conn)
    if row is None or row["status"] == "removed":
        raise not_found("listing not found")
    if row["seller_id"] != user["user_id"]:
        raise forbidden("not_owner", "only the seller can change this listing")
    return row


# ---- browse ------------------------------------------------------------------------

@router.get("/listings")
def feed(
    user: CurrentUser,
    q: str | None = None,
    category: str | None = None,
    condition: Condition | None = None,
    min_price: int | None = Query(None, ge=0),
    max_price: int | None = Query(None, ge=0),
    sort: Literal["newest", "price_asc", "price_desc"] = "newest",
    page: int = Query(1, ge=1),
    limit: int = Query(20, ge=1, le=50),
) -> dict:
    """Active listings in the viewer's own hostel, filtered, sorted and paged."""
    params = {
        "hostel_id": user["hostel_id"], "q": (q or "").strip() or None, "category": category or None,
        "condition": condition, "min_price": min_price, "max_price": max_price,
        "limit": limit, "offset": (page - 1) * limit, "sort": sort,
    }
    key = cache.feed_key(user["hostel_id"], params)
    if key and (hit := cache.get_json(key)) is not None:
        return hit

    rows = db.fetch_all(
        f"""
        SELECT {LISTING_COLS}, count(*) OVER () AS total_count
        FROM v_listing_cards
        WHERE hostel_id = %(hostel_id)s AND status = 'active'
          AND (%(category)s::text  IS NULL OR category = %(category)s::text)
          AND (%(condition)s::text IS NULL OR item_condition = %(condition)s::text)
          AND (%(min_price)s::int  IS NULL OR price >= %(min_price)s::int)
          AND (%(max_price)s::int  IS NULL OR price <= %(max_price)s::int)
          AND (%(q)s::text IS NULL OR search_doc @@ websearch_to_tsquery('english', %(q)s::text))
        ORDER BY {_ORDER_BY[sort]}
        LIMIT %(limit)s OFFSET %(offset)s
        """,
        params,
    )
    total = rows[0]["total_count"] if rows else 0
    for r in rows:
        del r["total_count"]
    result = {"items": rows, "total": total, "page": page, "limit": limit}
    if key:
        cache.set_json(key, result, get_settings().feed_cache_seconds)
    return result


@router.get("/listings/{listing_id}")
def detail(listing_id: int, user: CurrentUser) -> dict:
    row = get_card(listing_id)
    if row is None or (row["status"] == "removed" and user["user_id"] != row["seller_id"] and user["role"] != "admin"):
        raise not_found("listing not found")
    photos = db.fetch_all(
        "SELECT photo_id, url, position FROM listing_photos WHERE listing_id = %s ORDER BY position", (listing_id,)
    )

    viewer: dict = {"is_seller": user["user_id"] == row["seller_id"]}
    if viewer["is_seller"]:
        viewer["pending_requests"] = db.fetch_one(
            "SELECT count(*) AS n FROM orders WHERE listing_id = %s AND status = 'requested'", (listing_id,)
        )["n"]
    else:
        check = db.fetch_one(
            "SELECT r AS reason, fn_reason_message(r) AS message FROM fn_buy_block_reason(%s, %s) AS r",
            (listing_id, user["user_id"]),
        )
        viewer["can_buy"] = check["reason"] is None
        viewer["block_reason"] = check["reason"]
        viewer["block_message"] = check["message"]
        viewer["my_order"] = db.fetch_one(
            "SELECT order_id, status FROM orders WHERE listing_id = %s AND buyer_id = %s ORDER BY order_id DESC LIMIT 1",
            (listing_id, user["user_id"]),
        )
        conv = db.fetch_one(
            "SELECT conversation_id FROM conversations WHERE listing_id = %s AND buyer_id = %s",
            (listing_id, user["user_id"]),
        )
        viewer["conversation_id"] = conv["conversation_id"] if conv else None
    return {**row, "photos": photos, "viewer": viewer}


@router.get("/users/me/listings")
def my_listings(
    user: CurrentUser,
    status: Literal["active", "reserved", "sold", "removed"] | None = None,
) -> list[dict]:
    """The caller's own listings (all statuses except removed unless asked for)."""
    return db.fetch_all(
        f"""
        SELECT {LISTING_COLS},
               (SELECT count(*) FROM orders o WHERE o.listing_id = v.listing_id AND o.status = 'requested') AS pending_requests
        FROM v_listing_cards v
        WHERE seller_id = %(me)s
          AND (CASE WHEN %(status)s::text IS NULL THEN status <> 'removed' ELSE status = %(status)s::text END)
        ORDER BY created_at DESC
        """,
        {"me": user["user_id"], "status": status},
    )


# ---- create / edit / remove ---------------------------------------------------------

@router.post("/listings", status_code=201)
def create_listing(body: ListingIn, user: CurrentUser) -> dict:
    # The listings_validate trigger rejects unverified sellers, sellers without a
    # UPI ID, and an allowed block outside the seller's hostel.
    row = db.fetch_one(
        """
        INSERT INTO listings (seller_id, category_id, title, description, item_condition, price,
                              available_from, available_until, allowed_block_id, allowed_year)
        VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
        RETURNING listing_id
        """,
        (user["user_id"], _category_id(body.category), body.title, body.description, body.condition, body.price,
         body.available_from, body.available_until, body.allowed_block_id, body.allowed_year),
    )
    cache.bump_feed(user["hostel_id"])
    return detail(row["listing_id"], user)


@router.patch("/listings/{listing_id}")
def update_listing(listing_id: int, body: ListingPatch, user: CurrentUser) -> dict:
    changes = body.model_dump(include=body.model_fields_set)
    if "category" in changes:
        changes["category_id"] = _category_id(changes.pop("category"))
    if "condition" in changes:
        changes["item_condition"] = changes.pop("condition")

    with db.transaction() as conn:
        current = _lock_own_listing(conn, listing_id, user)
        if current["status"] != "active":
            raise conflict("listing_locked", "a listing can only be edited while it is active")
        if changes:
            query = sql.SQL("UPDATE listings SET {} WHERE listing_id = %s").format(
                sql.SQL(", ").join(sql.SQL("{} = %s").format(sql.Identifier(k)) for k in changes)
            )
            conn.execute(query, [*changes.values(), listing_id])
    cache.bump_feed(user["hostel_id"])
    return detail(listing_id, user)


@router.delete("/listings/{listing_id}")
def remove_listing(listing_id: int, user: CurrentUser) -> dict:
    with db.transaction() as conn:
        current = _lock_own_listing(conn, listing_id, user)
        if current["status"] == "reserved":
            raise conflict("listing_reserved", "this item is reserved for a buyer; cancel or finish that order first")
        # The listings_close_requests trigger rejects everyone still waiting; app.actor_id
        # makes the audit trail record the seller as the one who caused it.
        conn.execute("SELECT set_config('app.actor_id', %s, true)", (str(user["user_id"]),))
        conn.execute("UPDATE listings SET status = 'removed' WHERE listing_id = %s", (listing_id,))
    cache.bump_feed(user["hostel_id"])
    return {"listing_id": listing_id, "status": "removed"}


# ---- photos ------------------------------------------------------------------------

MAX_PHOTOS = 5


@router.post("/listings/{listing_id}/photos", status_code=201)
def add_photo(listing_id: int, file: UploadFile, user: CurrentUser) -> dict:
    max_bytes = get_settings().max_upload_mb * 1024 * 1024
    data = file.file.read(max_bytes + 1)
    if len(data) > max_bytes:
        raise AppError(413, "file_too_large", f"photos must be at most {get_settings().max_upload_mb} MB")
    ext = storage.detect_image(data)
    if ext is None:
        raise invalid("file", "only JPEG, PNG or WebP images are allowed")

    with db.transaction() as conn:
        current = _lock_own_listing(conn, listing_id, user)
        if current["status"] not in ("active", "reserved"):
            raise conflict("listing_locked", "photos can't be changed on a sold listing")
        count = db.fetch_one("SELECT count(*) AS n FROM listing_photos WHERE listing_id = %s", (listing_id,), conn=conn)["n"]
        if count >= MAX_PHOTOS:
            raise conflict("too_many_photos", f"a listing can have at most {MAX_PHOTOS} photos")
        url, key = storage.save_photo(data, ext, user["user_id"])
        try:
            photo = db.fetch_one(
                """INSERT INTO listing_photos (listing_id, url, storage_key, position)
                   VALUES (%s, %s, %s, %s) RETURNING photo_id, url, position""",
                (listing_id, url, key, count),
                conn=conn,
            )
        except Exception:
            storage.delete_photo(key)
            raise
    cache.bump_feed(user["hostel_id"])
    return photo


@router.delete("/listings/{listing_id}/photos/{photo_id}")
def delete_photo(listing_id: int, photo_id: int, user: CurrentUser) -> dict:
    with db.transaction() as conn:
        _lock_own_listing(conn, listing_id, user)
        gone = db.fetch_one(
            "DELETE FROM listing_photos WHERE photo_id = %s AND listing_id = %s RETURNING position, storage_key",
            (photo_id, listing_id),
            conn=conn,
        )
        if gone is None:
            raise not_found("photo not found")
        # Close the gap; the unique (listing_id, position) check is deferred to commit.
        conn.execute(
            "UPDATE listing_photos SET position = position - 1 WHERE listing_id = %s AND position > %s",
            (listing_id, gone["position"]),
        )
    storage.delete_photo(gone["storage_key"])
    cache.bump_feed(user["hostel_id"])
    return {"photo_id": photo_id, "deleted": True}
