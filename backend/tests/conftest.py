"""Test setup.

Integration tests need TEST_DATABASE_URL: a separate, EMPTY PostgreSQL database
(for example a second database or branch on your hosting provider). The suite drops
and recreates every Hostel Market table in it. Without it, only unit tests run.
"""

import os
import sys
import tempfile
from pathlib import Path

import pytest

BACKEND = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(BACKEND))


def _read_dotenv(name: str) -> str:
    env = BACKEND / ".env"
    if env.exists():
        for line in env.read_text(encoding="utf-8").splitlines():
            key, sep, value = line.partition("=")
            if sep and key.strip() == name:
                return value.strip().strip('"').strip("'")
    return ""


TEST_DATABASE_URL = os.environ.get("TEST_DATABASE_URL") or _read_dotenv("TEST_DATABASE_URL")

# Configure the app BEFORE it is imported. DATABASE_URL always points at the test
# database (or nowhere), never at the real one from .env.
os.environ.update({
    "APP_ENV": "test",
    "DATABASE_URL": TEST_DATABASE_URL or "postgresql://invalid.localhost/none",
    "JWT_SECRET": "test-secret-test-secret-test-secret-123",
    "BCRYPT_ROUNDS": "4",
    "REDIS_URL": "",
    "CLOUDINARY_URL": "",
    "COLLEGE_EMAIL_DOMAINS": "college.edu",
    "UPLOAD_DIR": tempfile.mkdtemp(prefix="hm-uploads-"),
    "EXPIRE_JOB_SECONDS": "3600",
})

_TABLES = "messages, conversations, order_status_history, orders, listing_photos, listings, categories, users, blocks, hostels"

# Base data for every test: hostel 1 has blocks 1 (A) and 2 (B); hostel 2 has block 3 (A).
_BASE_DATA = """
INSERT INTO hostels (name) VALUES ('Hostel One'), ('Hostel Two');
INSERT INTO blocks (hostel_id, name) VALUES (1, 'A'), (1, 'B'), (2, 'A');
INSERT INTO categories (slug, name) VALUES ('books', 'Books'), ('electronics', 'Electronics'), ('other', 'Other');
"""


@pytest.fixture(scope="session")
def db_url() -> str:
    if not TEST_DATABASE_URL:
        pytest.skip("set TEST_DATABASE_URL to run the integration tests")
    from scripts.init_db import apply

    apply(TEST_DATABASE_URL, reset=True, seed=False)
    return TEST_DATABASE_URL


@pytest.fixture(scope="session")
def _client(db_url):
    from fastapi.testclient import TestClient

    from app.main import app

    with TestClient(app) as c:
        yield c


@pytest.fixture
def sql(db_url):
    """Run SQL directly against the test database."""
    import psycopg
    from psycopg.rows import dict_row

    with psycopg.connect(db_url, autocommit=True, row_factory=dict_row, prepare_threshold=None) as conn:
        yield conn


@pytest.fixture
def api(_client, sql):
    """A clean database plus an HTTP client for the app."""
    sql.execute(f"TRUNCATE {_TABLES} RESTART IDENTITY CASCADE")
    sql.execute(_BASE_DATA)
    return _client


class User(dict):
    @property
    def id(self) -> int:
        return self["user"]["user_id"]

    @property
    def h(self) -> dict:
        return {"Authorization": f"Bearer {self['access_token']}"}


@pytest.fixture
def make_user(api):
    counter = {"n": 0}

    def _make(name: str = "", *, block_id: int = 1, year: int = 2, upi: str | None = "seller@okaxis",
              verified: bool = True, password: str = "password123") -> User:
        counter["n"] += 1
        name = name or f"user{counter['n']}"
        domain = "college.edu" if verified else "gmail.com"
        body = {"email": f"{name}@{domain}", "password": password, "full_name": name.title(),
                "block_id": block_id, "year_of_study": year}
        if upi:
            body["upi_id"] = upi
        r = api.post("/api/auth/register", json=body)
        assert r.status_code == 201, r.text
        return User(r.json())

    return _make


@pytest.fixture
def make_listing(api):
    def _make(seller: User, **overrides) -> dict:
        body = {"title": "Physics textbook", "description": "HC Verma part 1", "category": "books",
                "condition": "good", "price": 300, **overrides}
        r = api.post("/api/listings", json=body, headers=seller.h)
        assert r.status_code == 201, r.text
        return r.json()

    return _make
