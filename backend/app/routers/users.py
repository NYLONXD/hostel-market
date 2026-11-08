from fastapi import APIRouter
from psycopg import sql

from .. import cache, db
from ..deps import CurrentUser
from ..errors import invalid, not_found
from ..schemas import ProfilePatch

router = APIRouter(prefix="/users", tags=["users"])


@router.get("/me")
def me(user: CurrentUser) -> dict:
    stats = db.fetch_one("SELECT * FROM v_seller_stats WHERE user_id = %s", (user["user_id"],))
    return {**user, "stats": stats}


@router.patch("/me")
def update_me(body: ProfilePatch, user: CurrentUser) -> dict:
    changes = body.model_dump(include=body.model_fields_set)
    if not changes:
        return user
    if "block_id" in changes and db.fetch_one("SELECT 1 FROM blocks WHERE block_id = %s", (changes["block_id"],)) is None:
        raise invalid("block_id", "unknown block")

    query = sql.SQL("UPDATE users SET {} WHERE user_id = %s").format(
        sql.SQL(", ").join(sql.SQL("{} = %s").format(sql.Identifier(k)) for k in changes)
    )
    db.execute(query, [*changes.values(), user["user_id"]])
    if "block_id" in changes:
        # Moving block may move the user's listings to another hostel's feed.
        cache.bump_feed(user["hostel_id"])
        cache.bump_feed(db.fetch_one("SELECT hostel_id FROM blocks WHERE block_id = %s", (changes["block_id"],))["hostel_id"])
    return db.fetch_one("SELECT * FROM v_user_profiles WHERE user_id = %s", (user["user_id"],))


@router.get("/{user_id}")
def public_profile(user_id: int, _: CurrentUser) -> dict:
    row = db.fetch_one(
        """
        SELECT p.user_id, p.full_name, p.is_verified, p.year_of_study, p.block_name, p.hostel_id, p.hostel_name,
               p.created_at, s.active_listings, s.sold_listings
        FROM v_user_profiles p
        JOIN v_seller_stats s ON s.user_id = p.user_id
        WHERE p.user_id = %s
        """,
        (user_id,),
    )
    if row is None:
        raise not_found("user not found")
    return row
