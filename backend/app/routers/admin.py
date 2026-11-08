from typing import Literal

from fastapi import APIRouter, Body, Query

from .. import cache, db
from ..deps import AdminUser
from ..errors import not_found
from ..schemas import BlockIn, CategoryIn, HostelIn, RemoveIn, VerifyIn
from .listings import LISTING_COLS, bump_listing_feed
from .meta import CATEGORIES_KEY, HOSTELS_KEY

router = APIRouter(prefix="/admin", tags=["admin"])


@router.get("/stats")
def stats(_: AdminUser) -> dict:
    """Dashboard numbers, all computed with aggregate queries and views."""
    totals = db.fetch_one(
        """
        SELECT
          (SELECT count(*) FROM users)                                  AS users,
          (SELECT count(*) FROM users WHERE NOT is_verified)            AS unverified_users,
          (SELECT count(*) FROM listings WHERE status = 'active')       AS active_listings,
          (SELECT count(*) FROM listings WHERE status = 'sold')         AS sold_listings,
          (SELECT count(*) FROM orders WHERE status = 'requested')      AS open_requests,
          (SELECT count(*) FROM orders WHERE status = 'completed')      AS completed_orders,
          (SELECT COALESCE(sum(price), 0) FROM orders
            WHERE status IN ('paid', 'handed_over', 'completed'))       AS total_sales_value
        """
    )
    orders_by_status = db.fetch_all("SELECT status, count(*) AS count FROM orders GROUP BY status ORDER BY count DESC")
    by_hostel = db.fetch_all(
        """
        SELECT h.hostel_id, h.name AS hostel_name,
               count(DISTINCT u.user_id)                                   AS residents,
               count(l.listing_id) FILTER (WHERE l.status = 'active')      AS active_listings
        FROM hostels h
        LEFT JOIN blocks b   ON b.hostel_id = h.hostel_id
        LEFT JOIN users u    ON u.block_id = b.block_id
        LEFT JOIN listings l ON l.seller_id = u.user_id
        GROUP BY h.hostel_id, h.name
        ORDER BY h.name
        """
    )
    categories = db.fetch_all(
        """SELECT category_name, sum(active_listings) AS active_listings, round(avg(avg_price)) AS avg_price
           FROM v_hostel_category_summary GROUP BY category_name ORDER BY active_listings DESC"""
    )
    top_sellers = db.fetch_all(
        "SELECT * FROM v_seller_stats WHERE sold_listings > 0 ORDER BY total_earned DESC, sold_listings DESC LIMIT 5"
    )
    return {
        "totals": totals, "orders_by_status": orders_by_status, "hostels": by_hostel,
        "categories": categories, "top_sellers": top_sellers,
    }


@router.get("/users")
def users(
    _: AdminUser,
    verified: bool | None = None,
    q: str | None = None,
    page: int = Query(1, ge=1),
    limit: int = Query(50, ge=1, le=100),
) -> dict:
    rows = db.fetch_all(
        """
        SELECT *, count(*) OVER () AS total_count FROM v_user_profiles
        WHERE (%(verified)s::boolean IS NULL OR is_verified = %(verified)s::boolean)
          AND (%(q)s::text IS NULL OR full_name ILIKE '%%' || %(q)s::text || '%%' OR email ILIKE '%%' || %(q)s::text || '%%')
        ORDER BY created_at DESC
        LIMIT %(limit)s OFFSET %(offset)s
        """,
        {"verified": verified, "q": (q or "").strip() or None, "limit": limit, "offset": (page - 1) * limit},
    )
    total = rows[0]["total_count"] if rows else 0
    for r in rows:
        del r["total_count"]
    return {"items": rows, "total": total, "page": page, "limit": limit}


@router.post("/users/{user_id}/verify")
def verify_user(user_id: int, _: AdminUser, body: VerifyIn | None = Body(None)) -> dict:
    db.execute("CALL sp_set_user_verified(%s, %s)", (user_id, (body or VerifyIn()).verified))
    return db.fetch_one("SELECT * FROM v_user_profiles WHERE user_id = %s", (user_id,))


@router.get("/listings")
def listings(
    _: AdminUser,
    status: Literal["active", "reserved", "sold", "removed"] | None = None,
    q: str | None = None,
    page: int = Query(1, ge=1),
    limit: int = Query(50, ge=1, le=100),
) -> dict:
    rows = db.fetch_all(
        f"""
        SELECT {LISTING_COLS}, count(*) OVER () AS total_count FROM v_listing_cards
        WHERE (%(status)s::text IS NULL OR status = %(status)s::text)
          AND (%(q)s::text IS NULL OR search_doc @@ websearch_to_tsquery('english', %(q)s::text))
        ORDER BY created_at DESC
        LIMIT %(limit)s OFFSET %(offset)s
        """,
        {"status": status, "q": (q or "").strip() or None, "limit": limit, "offset": (page - 1) * limit},
    )
    total = rows[0]["total_count"] if rows else 0
    for r in rows:
        del r["total_count"]
    return {"items": rows, "total": total, "page": page, "limit": limit}


@router.post("/listings/{listing_id}/remove")
def remove_listing(listing_id: int, admin: AdminUser, body: RemoveIn | None = Body(None)) -> dict:
    db.execute("CALL sp_admin_remove_listing(%s, %s, %s)", (listing_id, admin["user_id"], body.reason if body else None))
    bump_listing_feed(listing_id)
    return {"listing_id": listing_id, "status": "removed"}


@router.post("/hostels", status_code=201)
def add_hostel(body: HostelIn, _: AdminUser) -> dict:
    row = db.fetch_one("INSERT INTO hostels (name) VALUES (%s) RETURNING hostel_id, name", (body.name,))
    cache.delete(HOSTELS_KEY)
    return row


@router.post("/hostels/{hostel_id}/blocks", status_code=201)
def add_block(hostel_id: int, body: BlockIn, _: AdminUser) -> dict:
    if db.fetch_one("SELECT 1 FROM hostels WHERE hostel_id = %s", (hostel_id,)) is None:
        raise not_found("hostel not found")
    row = db.fetch_one(
        "INSERT INTO blocks (hostel_id, name) VALUES (%s, %s) RETURNING block_id, hostel_id, name", (hostel_id, body.name)
    )
    cache.delete(HOSTELS_KEY)
    return row


@router.post("/categories", status_code=201)
def add_category(body: CategoryIn, _: AdminUser) -> dict:
    row = db.fetch_one(
        "INSERT INTO categories (slug, name) VALUES (%s, %s) RETURNING category_id, slug, name", (body.slug, body.name)
    )
    cache.delete(CATEGORIES_KEY)
    return row
