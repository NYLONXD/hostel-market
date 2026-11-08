"""Optional Redis cache.

PostgreSQL is always the source of truth. Redis only keeps short-lived copies of
read-heavy results (feed pages, hostel and category lists). Without REDIS_URL, or
if Redis is down, every call simply falls through to the database.

Feed invalidation uses a version number per hostel: any change to a listing in a
hostel bumps feed:ver:<hostel_id>, so old cached pages are never read again and
expire on their own.
"""

import hashlib
import json
import logging
from typing import Any

import redis

log = logging.getLogger("hostel_market")

_client: redis.Redis | None = None


def init(url: str) -> None:
    global _client
    _client = redis.Redis.from_url(url, socket_timeout=1, socket_connect_timeout=2, decode_responses=True) if url else None


def close() -> None:
    global _client
    if _client is not None:
        _client.close()
        _client = None


def enabled() -> bool:
    return _client is not None


def ping() -> bool:
    if _client is None:
        return False
    try:
        return bool(_client.ping())
    except redis.RedisError:
        return False


def get_json(key: str) -> Any | None:
    if _client is None:
        return None
    try:
        raw = _client.get(key)
        return json.loads(raw) if raw is not None else None
    except (redis.RedisError, ValueError):
        log.warning("cache get failed for %s", key)
        return None


def set_json(key: str, value: Any, ttl_seconds: int) -> None:
    if _client is None:
        return
    try:
        _client.set(key, json.dumps(value, default=str), ex=ttl_seconds)
    except redis.RedisError:
        log.warning("cache set failed for %s", key)


def delete(*keys: str) -> None:
    if _client is None:
        return
    try:
        _client.delete(*keys)
    except redis.RedisError:
        log.warning("cache delete failed")


def feed_key(hostel_id: int, params: dict) -> str | None:
    """Cache key for one feed page, or None when caching is off."""
    if _client is None:
        return None
    try:
        version = _client.get(f"feed:ver:{hostel_id}") or "0"
    except redis.RedisError:
        return None
    digest = hashlib.sha256(json.dumps(params, sort_keys=True, default=str).encode()).hexdigest()[:32]
    return f"feed:{hostel_id}:{version}:{digest}"


def bump_feed(hostel_id: int | None) -> None:
    if _client is None or hostel_id is None:
        return
    try:
        _client.incr(f"feed:ver:{hostel_id}")
    except redis.RedisError:
        log.warning("cache invalidation failed for hostel %s", hostel_id)
