"""Public lookup data used by the sign-up form and filters."""

from fastapi import APIRouter

from .. import cache, db

router = APIRouter(prefix="/meta", tags=["meta"])

HOSTELS_KEY = "meta:hostels"
CATEGORIES_KEY = "meta:categories"
_TTL = 600


def hostels_with_blocks() -> list[dict]:
    cached = cache.get_json(HOSTELS_KEY)
    if cached is not None:
        return cached
    rows = db.fetch_all(
        """
        SELECT h.hostel_id, h.name,
               COALESCE(json_agg(json_build_object('block_id', b.block_id, 'name', b.name) ORDER BY b.name)
                          FILTER (WHERE b.block_id IS NOT NULL), '[]'::json) AS blocks
        FROM hostels h
        LEFT JOIN blocks b ON b.hostel_id = h.hostel_id
        GROUP BY h.hostel_id, h.name
        ORDER BY h.name
        """
    )
    cache.set_json(HOSTELS_KEY, rows, _TTL)
    return rows


def categories() -> list[dict]:
    cached = cache.get_json(CATEGORIES_KEY)
    if cached is not None:
        return cached
    rows = db.fetch_all("SELECT category_id, slug, name FROM categories ORDER BY name")
    cache.set_json(CATEGORIES_KEY, rows, _TTL)
    return rows


@router.get("/hostels")
def list_hostels() -> list[dict]:
    return hostels_with_blocks()


@router.get("/categories")
def list_categories() -> list[dict]:
    return categories()
