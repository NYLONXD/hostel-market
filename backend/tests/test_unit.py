"""Unit tests that need no database."""

from datetime import time

import pytest
from pydantic import ValidationError

from app.schemas import ListingIn, ListingPatch, ProfilePatch, RegisterIn
from app.security import create_access_token, decode_access_token, hash_password, verify_password
from app.storage import detect_image

BASE = {"title": "Lamp", "category": "other", "condition": "good", "price": 100}


def test_listing_window_needs_both_ends():
    with pytest.raises(ValidationError):
        ListingIn(**BASE, available_from=time(18, 0))
    with pytest.raises(ValidationError):
        ListingIn(**BASE, available_from=time(18, 0), available_until=time(18, 0))
    ok = ListingIn(**BASE, available_from=time(22, 0), available_until=time(6, 0))  # crosses midnight
    assert ok.available_until == time(6, 0)


def test_listing_price_and_title_limits():
    with pytest.raises(ValidationError):
        ListingIn(**{**BASE, "price": 0})
    with pytest.raises(ValidationError):
        ListingIn(**{**BASE, "title": "ab"})
    with pytest.raises(ValidationError):
        ListingIn(**BASE, unknown_field=1)


def test_listing_patch_rules():
    assert ListingPatch(allowed_year=None).model_fields_set == {"allowed_year"}  # clearing a rule is allowed
    with pytest.raises(ValidationError):
        ListingPatch(price=None)  # required fields can't be cleared
    with pytest.raises(ValidationError):
        ListingPatch(available_from=time(9, 0))  # window ends must come together


def test_profile_patch():
    assert ProfilePatch(upi_id=None).model_fields_set == {"upi_id"}
    with pytest.raises(ValidationError):
        ProfilePatch(full_name=None)
    with pytest.raises(ValidationError):
        ProfilePatch(upi_id="not-a-upi")


def test_register_validation():
    good = {"email": "a@college.edu", "password": "password123", "full_name": "A", "block_id": 1, "year_of_study": 2}
    RegisterIn(**good)
    for bad in ({"password": "short"}, {"year_of_study": 7}, {"email": "nope"}, {"upi_id": "x"}):
        with pytest.raises(ValidationError):
            RegisterIn(**{**good, **bad})


def test_password_hashing():
    h = hash_password("password123")
    assert verify_password("password123", h)
    assert not verify_password("wrong", h)
    assert verify_password("x" * 100, hash_password("x" * 100))  # >72 bytes doesn't crash


def test_tokens_round_trip():
    token, _ = create_access_token(42)
    assert decode_access_token(token) == 42
    assert decode_access_token(token + "x") is None
    assert decode_access_token("garbage") is None


def test_detect_image():
    assert detect_image(b"\xff\xd8\xff\xe0rest") == "jpg"
    assert detect_image(b"\x89PNG\r\n\x1a\nrest") == "png"
    assert detect_image(b"RIFF\x00\x00\x00\x00WEBPrest") == "webp"
    assert detect_image(b"GIF89a") is None
    assert detect_image(b"<svg") is None
