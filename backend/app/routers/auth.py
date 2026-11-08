from fastapi import APIRouter

from .. import db
from ..config import get_settings
from ..errors import AppError, invalid
from ..schemas import LoginIn, RegisterIn
from ..security import burn_password_check, create_access_token, hash_password, verify_password

router = APIRouter(prefix="/auth", tags=["auth"])


def _session(user_id: int) -> dict:
    token, expires = create_access_token(user_id)
    user = db.fetch_one("SELECT * FROM v_user_profiles WHERE user_id = %s", (user_id,))
    return {"access_token": token, "token_type": "bearer", "expires_at": expires, "user": user}


@router.post("/register", status_code=201)
def register(body: RegisterIn) -> dict:
    email = body.email.strip().lower()
    if db.fetch_one("SELECT 1 FROM blocks WHERE block_id = %s", (body.block_id,)) is None:
        raise invalid("block_id", "unknown block")
    # College email addresses are trusted and verified straight away; others wait for an admin.
    verified = email.rsplit("@", 1)[-1] in get_settings().college_domains
    row = db.fetch_one(
        """
        INSERT INTO users (email, password_hash, full_name, block_id, year_of_study, upi_id, is_verified, verified_at)
        VALUES (%s, %s, %s, %s, %s, %s, %s, CASE WHEN %s THEN now() END)
        RETURNING user_id
        """,
        (email, hash_password(body.password), body.full_name, body.block_id, body.year_of_study,
         body.upi_id, verified, verified),
    )
    return _session(row["user_id"])


@router.post("/login")
def login(body: LoginIn) -> dict:
    row = db.fetch_one("SELECT user_id, password_hash FROM users WHERE email = %s", (body.email.strip().lower(),))
    if row is None:
        burn_password_check(body.password)
        raise AppError(401, "invalid_credentials", "invalid email or password")
    if not verify_password(body.password, row["password_hash"]):
        raise AppError(401, "invalid_credentials", "invalid email or password")
    return _session(row["user_id"])
