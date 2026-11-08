"""PostgreSQL access: a connection pool and a few helpers for raw SQL.

The app uses plain SQL (no ORM) so every query can be read, explained and copied
into the project report. Rows come back as dicts.
"""

from collections.abc import Iterator
from contextlib import contextmanager
from typing import Any

import psycopg
from psycopg.rows import dict_row
from psycopg_pool import ConnectionPool

_pool: ConnectionPool | None = None


def open_pool(url: str, max_size: int = 10) -> None:
    global _pool
    if _pool is not None:
        return
    _pool = ConnectionPool(
        url,
        min_size=1,
        max_size=max_size,
        open=True,
        # autocommit: each statement is its own transaction unless we open one with
        # transaction(). prepare_threshold=None: no server-side prepared statements,
        # so hosted poolers in transaction mode (Neon, Supabase) work.
        kwargs={"row_factory": dict_row, "autocommit": True, "prepare_threshold": None},
    )


def close_pool() -> None:
    global _pool
    if _pool is not None:
        _pool.close()
        _pool = None


def pool() -> ConnectionPool:
    if _pool is None:
        raise RuntimeError("database pool is not open")
    return _pool


@contextmanager
def transaction() -> Iterator[psycopg.Connection]:
    """Run several statements atomically: all of them commit or none do."""
    with pool().connection() as conn:
        with conn.transaction():
            yield conn


def fetch_one(sql: str, params: Any = None, conn: psycopg.Connection | None = None) -> dict | None:
    if conn is not None:
        return conn.execute(sql, params).fetchone()
    with pool().connection() as c:
        return c.execute(sql, params).fetchone()


def fetch_all(sql: str, params: Any = None, conn: psycopg.Connection | None = None) -> list[dict]:
    if conn is not None:
        return conn.execute(sql, params).fetchall()
    with pool().connection() as c:
        return c.execute(sql, params).fetchall()


def execute(sql: str, params: Any = None, conn: psycopg.Connection | None = None) -> int:
    """Run a statement and return the number of affected rows."""
    if conn is not None:
        return conn.execute(sql, params).rowcount
    with pool().connection() as c:
        return c.execute(sql, params).rowcount
