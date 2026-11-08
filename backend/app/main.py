"""FastAPI application: wiring, startup/shutdown, and the request-expiry job.

Run locally:  uvicorn app.main:app --reload
API docs:     http://localhost:8000/docs
"""

import logging
import threading
from contextlib import asynccontextmanager

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from fastapi.staticfiles import StaticFiles

from . import cache, db, errors, storage
from .config import get_settings
from .routers import admin, auth, chat, listings, meta, orders, users

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("hostel_market")


def _expire_requests_loop(stop: threading.Event, every_seconds: int) -> None:
    """Every minute, expire buy requests the seller never answered (fn_expire_requests)."""
    while not stop.wait(every_seconds):
        try:
            n = db.fetch_one("SELECT fn_expire_requests() AS n")["n"]
            if n:
                log.info("expired %d unanswered buy request(s)", n)
        except Exception:
            log.exception("request expiry job failed")


@asynccontextmanager
async def lifespan(_: FastAPI):
    s = get_settings()
    db.open_pool(s.database_url, s.db_pool_max)
    cache.init(s.redis_url)
    stop = threading.Event()
    job = threading.Thread(target=_expire_requests_loop, args=(stop, s.expire_job_seconds), daemon=True)
    job.start()
    log.info("started (cache: %s, photos: %s)", "redis" if cache.enabled() else "off",
             "cloudinary" if storage.using_cloudinary() else "local disk")
    try:
        yield
    finally:
        stop.set()
        cache.close()
        db.close_pool()


def create_app() -> FastAPI:
    s = get_settings()
    app = FastAPI(title="Hostel Market API", version="2.0.0", lifespan=lifespan)
    errors.install(app)
    app.add_middleware(
        CORSMiddleware,
        allow_origins=s.cors_origin_list,
        allow_credentials=True,
        allow_methods=["*"],
        allow_headers=["*"],
    )

    for r in (auth.router, meta.router, users.router, listings.router, orders.router, chat.router, admin.router):
        app.include_router(r, prefix="/api")

    if not storage.using_cloudinary():
        app.mount("/uploads", StaticFiles(directory=storage.local_dir()), name="uploads")

    @app.get("/api/health", tags=["meta"])
    def health() -> dict:
        try:
            db.fetch_one("SELECT 1")
            database = "ok"
        except Exception:
            database = "unavailable"
        return {"status": "ok" if database == "ok" else "degraded", "database": database,
                "cache": ("ok" if cache.ping() else "unavailable") if cache.enabled() else "disabled"}

    return app


app = create_app()
