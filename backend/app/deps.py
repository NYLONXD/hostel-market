"""Request dependencies: who is calling, and are they allowed in."""

from typing import Annotated

from fastapi import Depends
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer

from . import db
from .errors import forbidden, unauthorized
from .security import decode_access_token

_bearer = HTTPBearer(auto_error=False)


def current_user(creds: Annotated[HTTPAuthorizationCredentials | None, Depends(_bearer)]) -> dict:
    if creds is None:
        raise unauthorized()
    user_id = decode_access_token(creds.credentials)
    if user_id is None:
        raise unauthorized("your session has expired, sign in again")
    # Read fresh on every request so role and verification changes apply immediately.
    user = db.fetch_one("SELECT * FROM v_user_profiles WHERE user_id = %s", (user_id,))
    if user is None:
        raise unauthorized("account not found")
    return user


def admin_user(user: Annotated[dict, Depends(current_user)]) -> dict:
    if user["role"] != "admin":
        raise forbidden("admin_only", "only admins can do this")
    return user


CurrentUser = Annotated[dict, Depends(current_user)]
AdminUser = Annotated[dict, Depends(admin_user)]
