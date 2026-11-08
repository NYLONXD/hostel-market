"""One JSON error format for the whole API:

    {"error": {"code": "own_listing", "message": "you cannot buy your own listing"}}

Errors raised inside PostgreSQL by fn_raise() arrive with SQLSTATE 'HMxxx', where
xxx is the HTTP status, and DETAIL holding the code.
"""

import logging

import psycopg
from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from starlette.exceptions import HTTPException as StarletteHTTPException

log = logging.getLogger("hostel_market")


class AppError(Exception):
    def __init__(self, status: int, code: str, message: str, fields: list[dict] | None = None):
        super().__init__(message)
        self.status, self.code, self.message, self.fields = status, code, message, fields


def bad_request(code: str, message: str) -> AppError:
    return AppError(400, code, message)


def unauthorized(message: str = "sign in to continue") -> AppError:
    return AppError(401, "unauthorized", message)


def forbidden(code: str, message: str) -> AppError:
    return AppError(403, code, message)


def not_found(message: str = "not found") -> AppError:
    return AppError(404, "not_found", message)


def conflict(code: str, message: str) -> AppError:
    return AppError(409, code, message)


def invalid(field: str, message: str) -> AppError:
    return AppError(422, "validation_failed", "request is invalid", [{"field": field, "message": message}])


def _body(code: str, message: str, fields: list[dict] | None = None) -> dict:
    err: dict = {"code": code, "message": message}
    if fields:
        err["fields"] = fields
    return {"error": err}


# Messages for constraint names, so a database rule failure reads well in the UI.
_CONSTRAINT_MESSAGES = {
    "users_email_key": (409, "email_taken", "an account with this email already exists"),
    "orders_one_live_per_listing": (409, "listing_reserved", "this item is already reserved for another buyer"),
    "orders_one_request_per_buyer": (409, "already_requested", "you already have a pending request for this item"),
    "hostels_name_key": (409, "duplicate", "a hostel with this name already exists"),
    "blocks_unique_name_per_hostel": (409, "duplicate", "this hostel already has a block with this name"),
    "categories_slug_key": (409, "duplicate", "a category with this slug already exists"),
    "categories_name_key": (409, "duplicate", "a category with this name already exists"),
}


def from_db_error(exc: psycopg.Error) -> AppError | None:
    """Translate a PostgreSQL error into an AppError, or None if it is a real bug."""
    state = exc.sqlstate or ""
    diag = exc.diag
    if state.startswith("HM") and state[2:].isdigit():
        return AppError(int(state[2:]), diag.message_detail or "error", diag.message_primary or "request failed")
    if diag.constraint_name in _CONSTRAINT_MESSAGES:
        status, code, message = _CONSTRAINT_MESSAGES[diag.constraint_name]
        return AppError(status, code, message)
    if state == "23505":  # unique_violation
        return AppError(409, "duplicate", "this record already exists")
    if state == "23503":  # foreign_key_violation
        return AppError(422, "invalid_reference", "a referenced record does not exist")
    if state in ("23514", "23502", "22001"):  # check / not null / value too long
        return AppError(422, "constraint_violation", diag.message_primary or "a value is not allowed")
    return None


def install(app: FastAPI) -> None:
    @app.exception_handler(AppError)
    async def _app_error(_: Request, exc: AppError) -> JSONResponse:
        return JSONResponse(_body(exc.code, exc.message, exc.fields), status_code=exc.status)

    @app.exception_handler(psycopg.Error)
    async def _db_error(_: Request, exc: psycopg.Error) -> JSONResponse:
        mapped = from_db_error(exc)
        if mapped is None:
            log.exception("database error", exc_info=exc)
            return JSONResponse(_body("internal", "something went wrong"), status_code=500)
        return JSONResponse(_body(mapped.code, mapped.message, mapped.fields), status_code=mapped.status)

    @app.exception_handler(RequestValidationError)
    async def _validation(_: Request, exc: RequestValidationError) -> JSONResponse:
        fields = [
            {"field": ".".join(str(p) for p in e["loc"] if p not in ("body", "query", "path")), "message": e["msg"]}
            for e in exc.errors()
        ]
        return JSONResponse(_body("validation_failed", "request is invalid", fields), status_code=422)

    @app.exception_handler(StarletteHTTPException)
    async def _http(_: Request, exc: StarletteHTTPException) -> JSONResponse:
        code = {404: "not_found", 405: "method_not_allowed"}.get(exc.status_code, "error")
        return JSONResponse(_body(code, str(exc.detail)), status_code=exc.status_code)
