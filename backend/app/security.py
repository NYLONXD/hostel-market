"""Password hashing (bcrypt) and access tokens (JWT, HS256)."""

from datetime import UTC, datetime, timedelta

import bcrypt
import jwt

from .config import get_settings


def _pw_bytes(password: str) -> bytes:
    # bcrypt only looks at the first 72 bytes; cut explicitly so long passwords don't error.
    return password.encode("utf-8")[:72]


def hash_password(password: str) -> str:
    rounds = get_settings().bcrypt_rounds
    return bcrypt.hashpw(_pw_bytes(password), bcrypt.gensalt(rounds)).decode()


def verify_password(password: str, password_hash: str) -> bool:
    try:
        return bcrypt.checkpw(_pw_bytes(password), password_hash.encode())
    except ValueError:
        return False


# Compared against on unknown emails so login time does not reveal which emails exist.
_DUMMY_HASH: str | None = None


def burn_password_check(password: str) -> None:
    global _DUMMY_HASH
    if _DUMMY_HASH is None:
        _DUMMY_HASH = hash_password("not-a-real-password")
    verify_password(password, _DUMMY_HASH)


def create_access_token(user_id: int) -> tuple[str, datetime]:
    s = get_settings()
    expires = datetime.now(UTC) + timedelta(minutes=s.jwt_expire_minutes)
    token = jwt.encode({"sub": str(user_id), "exp": expires, "iat": datetime.now(UTC)}, s.jwt_secret, algorithm="HS256")
    return token, expires


def decode_access_token(token: str) -> int | None:
    """Return the user id inside a valid token, or None."""
    try:
        claims = jwt.decode(token, get_settings().jwt_secret, algorithms=["HS256"], options={"require": ["sub", "exp"]})
        return int(claims["sub"])
    except (jwt.PyJWTError, ValueError, KeyError):
        return None
