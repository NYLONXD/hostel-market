"""Integration tests: real HTTP requests against a real PostgreSQL (TEST_DATABASE_URL)."""

from datetime import UTC, datetime, timedelta, timezone

PNG = b"\x89PNG\r\n\x1a\n" + b"\x00" * 64
IST = timezone(timedelta(hours=5, minutes=30))


def err(r) -> str:
    return r.json()["error"]["code"]


# ---- auth -----------------------------------------------------------------------

def test_register_login_me(api, make_user):
    u = make_user("aman")
    assert u["user"]["is_verified"] is True  # college domain
    assert u["user"]["hostel_name"] == "Hostel One"

    r = api.post("/api/auth/login", json={"email": "AMAN@college.edu", "password": "password123"})
    assert r.status_code == 200
    me = api.get("/api/users/me", headers={"Authorization": f"Bearer {r.json()['access_token']}"}).json()
    assert me["email"] == "aman@college.edu" and "password_hash" not in me

    assert err(api.post("/api/auth/login", json={"email": "aman@college.edu", "password": "nope"})) == "invalid_credentials"
    assert err(api.post("/api/auth/login", json={"email": "ghost@college.edu", "password": "x"})) == "invalid_credentials"
    assert api.get("/api/users/me").status_code == 401


def test_duplicate_email_and_bad_block(api, make_user):
    make_user("aman")
    body = {"email": "aman@college.edu", "password": "password123", "full_name": "X", "block_id": 1, "year_of_study": 1}
    r = api.post("/api/auth/register", json=body)
    assert r.status_code == 409 and err(r) == "email_taken"
    r = api.post("/api/auth/register", json={**body, "email": "new@college.edu", "block_id": 99})
    assert r.status_code == 422


def test_meta(api):
    hostels = api.get("/api/meta/hostels").json()
    assert [h["name"] for h in hostels] == ["Hostel One", "Hostel Two"]
    assert [b["name"] for b in hostels[0]["blocks"]] == ["A", "B"]
    assert len(api.get("/api/meta/categories").json()) == 3


def test_profile_update(api, make_user):
    u = make_user("aman", upi=None)
    r = api.patch("/api/users/me", json={"upi_id": "aman@oksbi", "block_id": 3}, headers=u.h)
    assert r.status_code == 200
    assert r.json()["upi_id"] == "aman@oksbi" and r.json()["hostel_name"] == "Hostel Two"
    assert api.patch("/api/users/me", json={"upi_id": None}, headers=u.h).json()["upi_id"] is None


# ---- listings -------------------------------------------------------------------

def test_who_can_sell(api, make_user, make_listing):
    unverified = make_user("rohan", verified=False)
    r = api.post("/api/listings", json={"title": "Lamp", "category": "other", "condition": "good", "price": 100},
                 headers=unverified.h)
    assert r.status_code == 403 and err(r) == "not_verified"

    no_upi = make_user("kabir", upi=None)
    r = api.post("/api/listings", json={"title": "Lamp", "category": "other", "condition": "good", "price": 100},
                 headers=no_upi.h)
    assert r.status_code == 403 and err(r) == "upi_required"

    seller = make_user("aman")
    r = api.post("/api/listings", json={"title": "Lamp", "category": "nope", "condition": "good", "price": 100},
                 headers=seller.h)
    assert r.status_code == 422
    r = api.post("/api/listings", json={"title": "Lamp", "category": "other", "condition": "good", "price": 100,
                                        "allowed_block_id": 3}, headers=seller.h)  # block of another hostel
    assert r.status_code == 422 and err(r) == "invalid_block"
    assert make_listing(seller)["status"] == "active"


def test_feed_is_scoped_filtered_and_sorted(api, make_user, make_listing):
    a = make_user("aman")
    other_hostel = make_user("sneha", block_id=3)
    make_listing(a, title="Calculus book", price=500)
    make_listing(a, title="Bluetooth speaker", category="electronics", price=900)
    make_listing(other_hostel, title="Chemistry book", price=200)

    feed = api.get("/api/listings", headers=a.h).json()
    assert feed["total"] == 2
    assert {i["title"] for i in feed["items"]} == {"Calculus book", "Bluetooth speaker"}

    assert api.get("/api/listings?category=electronics", headers=a.h).json()["total"] == 1
    assert api.get("/api/listings?q=calculus", headers=a.h).json()["items"][0]["title"] == "Calculus book"
    assert api.get("/api/listings?max_price=600", headers=a.h).json()["total"] == 1
    prices = [i["price"] for i in api.get("/api/listings?sort=price_desc", headers=a.h).json()["items"]]
    assert prices == [900, 500]
    page2 = api.get("/api/listings?limit=1&page=2", headers=a.h).json()
    assert len(page2["items"]) == 1 and page2["total"] == 2


def test_edit_and_remove(api, make_user, make_listing):
    s, b = make_user("aman"), make_user("riya")
    l = make_listing(s)
    lid = l["listing_id"]

    r = api.patch(f"/api/listings/{lid}", json={"price": 250, "allowed_year": 3}, headers=s.h)
    assert r.json()["price"] == 250 and r.json()["allowed_year"] == 3
    assert api.patch(f"/api/listings/{lid}", json={"allowed_year": None}, headers=s.h).json()["allowed_year"] is None
    assert api.patch(f"/api/listings/{lid}", json={"price": 1}, headers=b.h).status_code == 403

    order = api.post(f"/api/listings/{lid}/orders", headers=b.h).json()
    api.post(f"/api/orders/{order['order_id']}/accept", headers=s.h)
    assert err(api.patch(f"/api/listings/{lid}", json={"price": 1}, headers=s.h)) == "listing_locked"
    assert err(api.delete(f"/api/listings/{lid}", headers=s.h)) == "listing_reserved"

    api.post(f"/api/orders/{order['order_id']}/cancel", headers=b.h)
    assert api.delete(f"/api/listings/{lid}", headers=s.h).status_code == 200
    assert api.get(f"/api/listings/{lid}", headers=b.h).status_code == 404
    assert api.get(f"/api/listings/{lid}", headers=s.h).json()["status"] == "removed"


def test_photos(api, make_user, make_listing):
    s = make_user("aman")
    lid = make_listing(s)["listing_id"]
    ids = []
    for _ in range(5):
        r = api.post(f"/api/listings/{lid}/photos", files={"file": ("p.png", PNG, "image/png")}, headers=s.h)
        assert r.status_code == 201, r.text
        ids.append(r.json()["photo_id"])
    r = api.post(f"/api/listings/{lid}/photos", files={"file": ("p.png", PNG, "image/png")}, headers=s.h)
    assert err(r) == "too_many_photos"
    r = api.post(f"/api/listings/{lid}/photos", files={"file": ("x.gif", b"GIF89a....", "image/gif")}, headers=s.h)
    assert r.status_code == 422

    assert api.delete(f"/api/listings/{lid}/photos/{ids[1]}", headers=s.h).status_code == 200
    photos = api.get(f"/api/listings/{lid}", headers=s.h).json()["photos"]
    assert [p["position"] for p in photos] == [0, 1, 2, 3]
    assert api.get(photos[0]["url"].replace("http://localhost:8000", "")).status_code == 200


# ---- eligibility ------------------------------------------------------------------

def test_eligibility_rules(api, make_user, make_listing):
    s = make_user("aman", block_id=1, year=2)
    other_hostel = make_user("sneha", block_id=3)
    block_b = make_user("riya", block_id=2, year=3)
    first_year = make_user("kabir", block_id=1, year=1)
    unverified = make_user("rohan", verified=False)

    def attempt(user, listing):
        return api.post(f"/api/listings/{listing['listing_id']}/orders", headers=user.h)

    plain = make_listing(s)
    assert err(attempt(s, plain)) == "own_listing"
    assert err(attempt(other_hostel, plain)) == "different_hostel"
    assert err(attempt(unverified, plain)) == "not_verified"

    only_block_a = make_listing(s, allowed_block_id=1)
    assert err(attempt(block_b, only_block_a)) == "block_restricted"
    assert attempt(first_year, only_block_a).status_code == 201

    only_year_3 = make_listing(s, allowed_year=3)
    assert err(attempt(first_year, only_year_3)) == "year_restricted"
    assert attempt(block_b, only_year_3).status_code == 201

    now = datetime.now(IST)
    closed = make_listing(s, available_from=(now + timedelta(hours=2)).strftime("%H:%M"),
                          available_until=(now + timedelta(hours=3)).strftime("%H:%M"))
    assert err(attempt(block_b, closed)) == "outside_time_window"
    opened = make_listing(s, available_from=(now - timedelta(hours=1)).strftime("%H:%M"),
                          available_until=(now + timedelta(hours=1)).strftime("%H:%M"))
    assert attempt(block_b, opened).status_code == 201

    detail = api.get(f"/api/listings/{only_year_3['listing_id']}", headers=first_year.h).json()
    assert detail["viewer"]["can_buy"] is False and detail["viewer"]["block_reason"] == "year_restricted"


# ---- orders ------------------------------------------------------------------------

def test_full_order_lifecycle(api, make_user, make_listing, sql):
    s, b, waiting = make_user("aman"), make_user("riya"), make_user("kabir")
    lid = make_listing(s, price=350)["listing_id"]

    o = api.post(f"/api/listings/{lid}/orders", json={"note": "tonight?"}, headers=b.h).json()
    assert o["status"] == "requested" and o["price"] == 350 and o["buyer_note"] == "tonight?"
    assert err(api.post(f"/api/listings/{lid}/orders", headers=b.h)) == "already_requested"
    w = api.post(f"/api/listings/{lid}/orders", headers=waiting.h).json()

    oid = o["order_id"]
    assert err(api.post(f"/api/orders/{oid}/accept", headers=b.h)) == "wrong_party"
    steps = [("accept", s, None), ("choose_payment", b, {"payment_method": "upi"}),
             ("confirm_payment", s, None), ("handover", s, None), ("complete", b, None)]
    for action, who, body in steps:
        r = api.post(f"/api/orders/{oid}/{action}", json=body, headers=who.h)
        assert r.status_code == 200, (action, r.text)
    final = r.json()
    assert final["status"] == "completed" and final["payment_method"] == "upi"
    assert [h["new_status"] for h in final["history"]] == [
        "requested", "accepted", "awaiting_payment", "paid", "handed_over", "completed"]
    assert final["history"][1]["changed_by"] == s.id

    assert api.get(f"/api/listings/{lid}", headers=s.h).json()["status"] == "sold"
    waited = api.get(f"/api/orders/{w['order_id']}", headers=waiting.h).json()
    assert waited["status"] == "rejected" and waited["close_reason"] == "item sold to another buyer"
    assert err(api.post(f"/api/orders/{oid}/complete", headers=b.h)) == "invalid_transition"

    selling = api.get("/api/orders?role=selling", headers=s.h).json()
    assert {x["order_id"] for x in selling} == {oid, w["order_id"]}
    assert api.get("/api/orders?role=buying", headers=s.h).json() == []


def test_only_one_buyer_gets_the_item(api, make_user, make_listing):
    s, b1, b2 = make_user("aman"), make_user("riya"), make_user("kabir")
    lid = make_listing(s)["listing_id"]
    o1 = api.post(f"/api/listings/{lid}/orders", headers=b1.h).json()["order_id"]
    o2 = api.post(f"/api/listings/{lid}/orders", headers=b2.h).json()["order_id"]

    assert api.post(f"/api/orders/{o1}/accept", headers=s.h).json()["status"] == "accepted"
    assert err(api.post(f"/api/orders/{o2}/accept", headers=s.h)) == "listing_reserved"
    assert api.get(f"/api/listings/{lid}", headers=s.h).json()["status"] == "reserved"

    # Seller cancels the live order -> item is back on sale and the waitlist can be accepted.
    r = api.post(f"/api/orders/{o1}/cancel", json={"reason": "buyer didn't show up"}, headers=s.h)
    assert r.json()["status"] == "cancelled" and r.json()["close_reason"] == "buyer didn't show up"
    assert api.get(f"/api/listings/{lid}", headers=s.h).json()["status"] == "active"
    assert api.post(f"/api/orders/{o2}/accept", headers=s.h).json()["status"] == "accepted"


def test_reject_cancel_and_privacy(api, make_user, make_listing):
    s, b, stranger = make_user("aman"), make_user("riya"), make_user("kabir")
    lid = make_listing(s)["listing_id"]
    oid = api.post(f"/api/listings/{lid}/orders", headers=b.h).json()["order_id"]

    assert api.get(f"/api/orders/{oid}", headers=stranger.h).status_code == 404
    assert api.post(f"/api/orders/{oid}/accept", headers=stranger.h).status_code == 404
    assert err(api.post(f"/api/orders/{oid}/cancel", headers=s.h)) == "use_reject"
    r = api.post(f"/api/orders/{oid}/reject", json={"reason": "sold offline"}, headers=s.h)
    assert r.json()["status"] == "rejected"
    assert api.post("/api/orders/999999/accept", headers=s.h).status_code == 404
    assert api.post(f"/api/orders/{oid}/explode", headers=s.h).status_code == 422


def test_requests_expire(api, make_user, make_listing, sql):
    s, b = make_user("aman"), make_user("riya")
    lid = make_listing(s)["listing_id"]
    oid = api.post(f"/api/listings/{lid}/orders", headers=b.h).json()["order_id"]
    sql.execute("UPDATE orders SET request_expires_at = now() - interval '1 minute' WHERE order_id = %s", (oid,))

    assert err(api.post(f"/api/orders/{oid}/accept", headers=s.h)) == "request_expired"
    assert sql.execute("SELECT fn_expire_requests() AS n").fetchone()["n"] == 1
    o = api.get(f"/api/orders/{oid}", headers=b.h).json()
    assert o["status"] == "expired" and o["history"][-1]["changed_by"] is None


def test_database_rejects_bad_rows_directly(api, make_user, make_listing, sql):
    import psycopg

    s = make_user("aman")
    lid = make_listing(s)["listing_id"]
    for stmt, params in [
        ("INSERT INTO orders (listing_id, buyer_id, price, request_expires_at) VALUES (%s, %s, 10, now())", (lid, s.id)),
        ("UPDATE listings SET price = -1 WHERE listing_id = %s", (lid,)),
        ("INSERT INTO orders (listing_id, buyer_id, price, request_expires_at) VALUES (999, %s, 10, now())", (s.id,)),
    ]:
        try:
            sql.execute(stmt, params)
        except psycopg.Error:
            continue
        raise AssertionError(f"database accepted: {stmt}")


# ---- chat ------------------------------------------------------------------------

def test_chat(api, make_user, make_listing):
    s, b, stranger = make_user("aman"), make_user("riya"), make_user("kabir")
    lid = make_listing(s)["listing_id"]

    assert err(api.post(f"/api/listings/{lid}/conversations", headers=s.h)) == "own_listing"
    conv = api.post(f"/api/listings/{lid}/conversations", headers=b.h).json()
    assert api.post(f"/api/listings/{lid}/conversations", headers=b.h).json()["conversation_id"] == conv["conversation_id"]
    cid = conv["conversation_id"]

    m1 = api.post(f"/api/conversations/{cid}/messages", json={"body": "  still available?  "}, headers=b.h).json()
    assert m1["body"] == "still available?"
    api.post(f"/api/conversations/{cid}/messages", json={"body": "yes"}, headers=s.h)
    api.post(f"/api/conversations/{cid}/messages", json={"body": "great"}, headers=b.h)

    assert api.get("/api/conversations/unread-count", headers=s.h).json()["unread"] == 1
    listed = api.get("/api/conversations", headers=s.h).json()
    assert listed[0]["last_message"] == "great" and listed[0]["other_name"] == "Riya"

    page = api.get(f"/api/conversations/{cid}/messages", headers=s.h).json()
    assert [m["body"] for m in page["messages"]] == ["still available?", "yes", "great"]
    newer = api.get(f"/api/conversations/{cid}/messages?after_id={m1['message_id']}", headers=s.h).json()
    assert len(newer["messages"]) == 2

    last_id = page["messages"][-1]["message_id"]
    api.post(f"/api/conversations/{cid}/read", json={"up_to_id": last_id + 100}, headers=s.h)
    assert api.get("/api/conversations/unread-count", headers=s.h).json()["unread"] == 0

    assert api.get(f"/api/conversations/{cid}/messages", headers=stranger.h).status_code == 404
    r = api.post(f"/api/conversations/{cid}/messages", json={"body": "hi"}, headers=stranger.h)
    assert r.status_code == 404


# ---- admin ------------------------------------------------------------------------

def test_admin(api, make_user, make_listing, sql):
    admin, s, b = make_user("boss"), make_user("aman"), make_user("riya")
    newbie = make_user("rohan", verified=False)
    sql.execute("UPDATE users SET role = 'admin' WHERE user_id = %s", (admin.id,))

    assert api.get("/api/admin/stats", headers=s.h).status_code == 403
    pending = api.get("/api/admin/users?verified=false", headers=admin.h).json()
    assert [u["user_id"] for u in pending["items"]] == [newbie.id]
    assert api.post(f"/api/admin/users/{newbie.id}/verify", json={"verified": True}, headers=admin.h).json()["is_verified"]

    lid = make_listing(s)["listing_id"]
    oid = api.post(f"/api/listings/{lid}/orders", headers=b.h).json()["order_id"]
    api.post(f"/api/orders/{oid}/accept", headers=s.h)
    r = api.post(f"/api/admin/listings/{lid}/remove", json={"reason": "not allowed"}, headers=admin.h)
    assert r.status_code == 200
    o = api.get(f"/api/orders/{oid}", headers=b.h).json()
    assert o["status"] == "cancelled" and o["close_reason"] == "not allowed"

    stats = api.get("/api/admin/stats", headers=admin.h).json()
    assert stats["totals"]["users"] == 4

    assert api.post("/api/admin/hostels", json={"name": "Hostel Three"}, headers=admin.h).status_code == 201
    assert api.post("/api/admin/hostels/1/blocks", json={"name": "c"}, headers=admin.h).json()["name"] == "C"
    assert err(api.post("/api/admin/hostels/1/blocks", json={"name": "A"}, headers=admin.h)) == "duplicate"
    assert any(h["name"] == "Hostel Three" for h in api.get("/api/meta/hostels").json())


def test_health(api):
    assert api.get("/api/health").json()["database"] == "ok"


def test_seed_file_loads(db_url):
    """The demo data and every sample query must run on a fresh schema."""
    import psycopg

    from scripts.init_db import SQL_DIR, apply

    apply(db_url, reset=True, seed=True)
    try:
        with psycopg.connect(db_url, autocommit=True, prepare_threshold=None) as conn:
            assert conn.execute("SELECT status FROM orders WHERE order_id = 1").fetchone()[0] == "completed"
            assert conn.execute("SELECT count(*) FROM order_status_history WHERE order_id = 1").fetchone()[0] == 6
            text = (SQL_DIR / "05_sample_queries.sql").read_text(encoding="utf-8")
            # Q19 is a deliberate failing transaction for psql; run everything else.
            text = text.replace("BEGIN;\nSELECT fn_request_order(2, 4, 'second try');\nROLLBACK;", "")
            conn.execute(text)
    finally:
        apply(db_url, reset=True, seed=False)
