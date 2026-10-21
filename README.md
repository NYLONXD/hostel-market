# Hostel Market API

Backend for a marketplace where students buy and sell used items (books, cycles,
appliances, ...) inside their hostel. Go **modular monolith**: one process, one
deployable, with domain modules that only talk to each other through small interfaces,
so any module can later become its own service.

- **Two sale modes per listing.** *Fixed price* (default): buyer requests, seller accepts,
  buyer pays the seller directly by UPI or cash, 0% fee. *Auction*: highest bid wins,
  the buyer pays the platform, the platform keeps `PLATFORM_FEE_PERCENT` (default 20%)
  and owes the seller the rest (ledger + payouts).
- **Stack:** Go 1.25, chi, PostgreSQL (pgx v5 + sqlc, golang-migrate), primary + optional
  read replica, Redis (go-redis v9) for rate limiting, caching, bid atomicity (Lua),
  WebSocket pub/sub and the asynq job queue, Cloudinary for photos, coder/websocket,
  golang-jwt, validator, slog, testify.

---

## Contents

1. [Project layout](#project-layout)
2. [Local setup](#local-setup)
3. [Configuration](#configuration)
4. [Running and testing](#running-and-testing)
5. [API overview](#api-overview)
6. [WebSocket protocol](#websocket-protocol)
7. [How the hard parts work](#how-the-hard-parts-work)
8. [Assumptions](#assumptions)
9. [Known limitations and next steps](#known-limitations-and-next-steps)

---

## Project layout

```
cmd/api/                main.go (process lifecycle), routes.go (wiring + router)
internal/
  config/               env loading/validation (.env supported)
  platform/
    db/                 primary/replica pools; Router = the one replica-vs-primary decision
    redis/              go-redis client
    ratelimit/          GCRA token bucket in Lua + middleware (429 + Retry-After)
    cache/              versioned Redis JSON cache (feed)
    jobs/               asynq client, worker, periodic tasks
    ws/                 Redis pub/sub hub + WebSocket gateway
    httpx/              JSON error envelope, decoding/validation, request ID, access log
    money/              integer-paise fee split (round half up)
    clock/ logx/ reqctx/ testutil/
  auth/ users/ listings/ media/ orders/ bidding/ chat/ payments/ admin/
migrations/             golang-migrate SQL (000001..000005)
sqlc.yaml               one generated store package per module
```

Each module owns its handlers (`handler.go`), service (`service.go`), types and
repository. The repository is the sqlc-generated `store/` package built from the
module's own `queries.sql`. Cross-module calls go through interfaces declared by the
caller (for example `orders.ListingStore` or `bidding.Awarder`), and `cmd/api/routes.go`
wires them together. Operations that must share one transaction pass a `pgx.Tx` /
`db.DBTX` through those interfaces.

---

## Local setup

No Docker. The commands below are for Windows (PowerShell); Linux/macOS equivalents are
the usual package-manager installs.

### 1. Tools

| Tool | Install |
|---|---|
| Go 1.25+ | <https://go.dev/dl/> |
| GNU make | `winget install ezwinports.make` (or `choco install make`) |
| migrate, golangci-lint | `make tools` (pinned versions that build with Go 1.25) |
| sqlc | Windows: download `sqlc_1.31.1_windows_amd64.zip` from <https://github.com/sqlc-dev/sqlc/releases> and put `sqlc.exe` on PATH (building from source needs cgo). Linux/macOS: `make tools`. |

`make` sets `GOTOOLCHAIN=local`, so Go never silently downloads a newer toolchain. Some
newer library releases need Go 1.26, so `go.mod` pins versions that build with 1.25.

### 2. PostgreSQL (14+)

```powershell
winget install PostgreSQL.PostgreSQL.17        # or the EDB installer
# add C:\Program Files\PostgreSQL\17\bin to PATH, then:
psql -U postgres -c "CREATE ROLE hostel LOGIN PASSWORD 'hostel';"
psql -U postgres -c "CREATE DATABASE hostel_market OWNER hostel;"
psql -U postgres -c "CREATE DATABASE hostel_market_test OWNER hostel;"   # for integration tests
```

No extensions are needed (`gen_random_uuid()` is built in).

### 3. Optional: a local read replica

Without `DATABASE_URL_REPLICA` every read goes to the primary, which is fine for
development. To exercise replica routing, start a streaming replica on port 5433:

```powershell
psql -U postgres -c "CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD 'replicator';"
# Allow it in the PRIMARY's pg_hba.conf, then reload:
#   host  replication  replicator  127.0.0.1/32  scram-sha-256
psql -U postgres -c "SELECT pg_reload_conf();"

pg_basebackup -h localhost -p 5432 -U replicator -D C:\pgdata-replica -R -X stream -P
# -R writes standby.signal + primary_conninfo. Change the replica's port:
Add-Content C:\pgdata-replica\postgresql.conf "port = 5433"
pg_ctl -D C:\pgdata-replica -l C:\pgdata-replica\replica.log start
```

Then set
`DATABASE_URL_REPLICA=postgres://hostel:hostel@localhost:5433/hostel_market?sslmode=disable`.

### 4. Redis (6.2+)

Any of these work: **Redis Cloud** (free tier), **Memurai** (Redis-compatible Windows
service), or Redis inside **WSL** (`sudo apt install redis-server`).

- Redis Cloud uses TLS: `REDIS_URL=rediss://default:<password>@<host>:<port>`.
- Hosted plans cap connections (the free tier allows 30). Each API process uses about
  `REDIS_POOL_SIZE` + 1 (pub/sub) connections, and the worker shares that pool. Keep the
  total across all instances under your plan's limit.
- Lua scripting (`EVAL`) and pub/sub must be available. They are on all of the above.

### 5. Cloudinary (photos)

1. Create an account at <https://cloudinary.com>, then copy **Cloud name**, **API Key**
   and **API Secret** from the dashboard into `.env` (`CLOUDINARY_*`).
2. Optionally change `CLOUDINARY_FOLDER` (default `hostel_market`).

Uploads never pass through this server:

1. The client calls `POST /v1/media/upload-signature`. The response contains `upload_url`,
   `api_key`, `timestamp`, `signature`, `folder`, `public_id` and `allowed_formats`.
2. The client POSTs `multipart/form-data` to `upload_url` with `file` plus exactly those
   fields.
3. The client sends the `public_id` from **Cloudinary's response** in `photo_public_ids`
   when creating or updating a listing.
4. The server checks with Cloudinary's Admin API that each asset exists, is an image of
   at most 10 MB, and sits in the caller's folder (`<folder>/users/<user_id>`). Both
   "fixed" and "dynamic" folder modes are handled.
5. Photos dropped from a listing, or belonging to a removed listing, are deleted in the
   background with retries.

Without Cloudinary credentials (development only), uploads return 503 and listings can
still be created without photos.

### 6. Configure, migrate, run

```powershell
Copy-Item .env.example .env     # then edit: DB URL, REDIS_URL, JWT secrets, Cloudinary
make migrate-up
make run                         # API on :8080, plus the job worker
```

**First admin.** Register normally, then promote the account:

```sql
UPDATE users SET role = 'admin', is_verified = true WHERE email = 'you@college.ac.in';
```

---

## Configuration

All settings are environment variables. A `.env` file is loaded if present, and real
environment variables override it. The app validates everything at startup and reports
every problem at once. See `.env.example` for the full annotated list. Key settings:

| Variable | Default | Notes |
|---|---|---|
| `APP_ENV` | `development` | `production` forbids the mock payment provider and requires Cloudinary |
| `DATABASE_URL_PRIMARY` | — | required |
| `DATABASE_URL_REPLICA` | empty | empty: all reads use the primary |
| `REDIS_URL` | — | `redis://` or `rediss://` |
| `REDIS_POOL_SIZE` / `WORKER_CONCURRENCY` | 10 / 4 | size these to your Redis plan |
| `JWT_ACCESS_SECRET` / `JWT_REFRESH_SECRET` | — | at least 32 bytes each, and different |
| `JWT_ACCESS_TTL` / `JWT_REFRESH_TTL` | 15m / 720h | |
| `COLLEGE_EMAIL_DOMAINS` | empty | registrations from these domains are auto-verified |
| `PLATFORM_FEE_PERCENT` | 20 | up to 2 decimals; stored on each order in basis points |
| `AUCTION_PAYMENT_DEADLINE` | 30m | time an auction winner has to pay |
| `PAYMENT_PENDING_GRACE` | 30m | extra time for a payment that is already pending |
| `ANTI_SNIPE_WINDOW` / `ANTI_SNIPE_EXTENSION` | 30s / 60s | |
| `ORDER_REQUEST_TTL` | 48h | an unanswered fixed-price request expires after this |
| `MAX_PAYMENT_DEFAULTS` | 2 | unpaid auction wins before a user is blocked from bidding |
| `RECENT_WRITE_TTL` / `FEED_CACHE_TTL` | 10s / 30s | |
| `RATE_LIMIT_*` | see `.env.example` | format `<requests>/<window>` |
| `PAYMENT_PROVIDER` | `mock` | `mock` or `upi_link` (needs `PLATFORM_UPI_ID`) |
| `TRUST_PROXY_HEADERS` | false | set true only behind a proxy that overwrites `X-Forwarded-For` |
| `WS_ALLOWED_ORIGINS` | empty | extra browser origins for WebSockets |

---

## Running and testing

```
make run            # API + worker (set RUN_WORKERS=false to run API-only instances)
make test           # all tests
make test-race      # with the race detector (needs a 64-bit C compiler for cgo)
make lint           # golangci-lint
make sqlc           # regenerate store packages after editing queries.sql or migrations
make migrate-up / migrate-down N=1 / migrate-new NAME=add_x
```

**Integration tests** need a real Postgres and Redis. They are **skipped** unless these
are set:

```
TEST_DATABASE_URL=postgres://hostel:hostel@localhost:5432/hostel_market_test?sslmode=disable
TEST_REDIS_URL=redis://localhost:6379/15
```

Each test gets its own schema with all migrations applied, so packages run in parallel
against one test database. Use a database you don't mind being written to. Redis tests
use random keys.

What is covered:

| Requirement | Test |
|---|---|
| Fee calculation, odd amounts | `platform/money` `TestSplitFee` |
| Eligibility windows incl. crossing midnight | `listings` `TestWindowContains`, `TestClockTimeUsesIST`, `TestCheckBuyer`; `orders` `TestRequestEnforcesEligibility` |
| Order locking | `orders` `TestOnlyOneBuyerWinsConcurrentAccepts` (10 concurrent accepts, 1 winner) |
| 100 concurrent bidders | `bidding` `TestHundredConcurrentBidders` |
| Auction close idempotency | `bidding` `TestCloseAuctionIsIdempotent` (10 concurrent closes, 1 order) |
| Rate limiting | `platform/ratelimit` (middleware, 429 + Retry-After, fail-open, atomic Lua under 100 goroutines, refill) |
| Auth guards | `auth` `TestGuards` (missing/forged/expired/`alg=none`/refresh-as-access/revoked tokens, stale admin claim, role checks), refresh rotation and reuse detection |
| Plus | next-bidder hand-off, anti-sniping, Redis revert/rebuild, payments (signatures, idempotency, settle-once, UTR never pays, payout gating, expiry hold, late payment), chat privacy/unread, end-to-end WebSocket delivery |

---

## API overview

Base path `/v1`. JSON everywhere. Money is always **integer paise** (`*_paise`).
Authenticate with `Authorization: Bearer <access_token>`. All routes require it except
health checks, register, login, refresh and the payment webhook.

**Errors** use one envelope everywhere:

```json
{"error": {"code": "outside_time_window", "message": "this listing can only be bought between 22:00 and 06:00 IST (it is 14:05 IST now)", "request_id": "6f1c...", "details": null}}
```

- Validation failures are `422 validation_failed`, with `details: [{field, rule, message}]`.
- Every response carries `X-Request-ID`.
- `429 rate_limited` comes with `Retry-After` and `RateLimit-*` headers.

**Pagination** is keyset-based: pass `next_cursor` back as `?cursor=`, with `limit` of 1–50
(up to 100–200 on some admin and chat lists).

### Health
| | |
|---|---|
| `GET /healthz` | liveness |
| `GET /readyz` | pings primary, replica and Redis; 503 if any is down |

### Auth and users
| | |
|---|---|
| `POST /auth/register` | `{email, password, name, hostel, block, year, upi_id?}` → session (rate-limited per IP) |
| `POST /auth/login` | `{email, password}` → session (rate-limited per email+IP) |
| `POST /auth/refresh` | `{refresh_token}` → new pair (rotation; reusing a spent token revokes the whole chain) |
| `POST /auth/logout` | `{refresh_token?}` revokes the access token (Redis blocklist) and the refresh chain |
| `GET/PATCH /users/me` | own profile; PATCH `{name?, block?, year?, upi_id?}` (`upi_id: null` clears it) |
| `GET /users/{id}` | public profile (no email or UPI ID) |

### Media and listings
| | |
|---|---|
| `POST /media/upload-signature` | Cloudinary signed-upload parameters (verified users) |
| `GET /listings` | own hostel's feed: `q` (full-text), `category`, `condition`, `bidding`, `min_price`, `max_price`, `sort=newest\|price_asc\|price_desc`, `cursor`, `limit`. Served from the replica and a Redis cache |
| `POST /listings` | create (verified seller with a UPI ID). Fixed: `price_paise`. Auction: `bidding_enabled:true, starting_price_paise, min_increment_paise, bid_ends_at`. Optional `available_from/until` (`"HH:MM"` IST), `allowed_block`, `allowed_year`, `photo_public_ids` (≤5) |
| `GET /listings/{id}` | detail with seller, rules, live auction state (from Redis) and `viewer_eligibility` |
| `PATCH /listings/{id}` | owner only, active listings only; bidding settings lock after the first bid |
| `DELETE /listings/{id}` | owner removes (not an auction with bids); photos deleted in the background |
| `GET /users/me/listings` | own listings, `?status=` |

### Orders (fixed price)
| | |
|---|---|
| `POST /listings/{id}/orders` | buyer requests `{note?}`; every eligibility rule checked |
| `POST /orders/{id}/accept` / `reject` | seller (accept reserves the item; only one buyer can win) |
| `POST /orders/{id}/payment-method` | buyer `{method: upi_direct\|cash}`; the response includes `payment_instructions` with a `upi://` link to the seller |
| `POST /orders/{id}/confirm-payment` | seller confirms receipt → `paid` (item sold, other requests rejected) |
| `POST /orders/{id}/handover` | seller → `handed_over` |
| `POST /orders/{id}/complete` | buyer confirms receipt → `completed` |
| `POST /orders/{id}/cancel` | `{reason?}`; buyer withdraws a request, or either side cancels before payment |
| `GET /orders`, `GET /orders/{id}` | participants only; `?role=buyer\|seller&status=` |

State machine: `requested → accepted → awaiting_payment → paid → handed_over → completed`,
with `requested → rejected | cancelled | expired`, `accepted → cancelled`, and
`awaiting_payment → cancelled | expired`. Auction orders start at `awaiting_payment`.

### Bidding
| | |
|---|---|
| `POST /listings/{id}/bids` | `{amount_paise}` (rate-limited per user) → bid plus `ends_at`, `extended`, `min_next_bid_paise` |
| `GET /listings/{id}/bids` | history, newest first, `?round=&before=&limit=` |
| `GET /listings/{id}/auction` | live state: highest bid from Redis, end time, minimum next bid, `server_time` |

### Payments (auction orders)
| | |
|---|---|
| `POST /orders/{id}/payments` | **requires `Idempotency-Key`**. Returns the payment and its checkout (`upi_uri` for `upi_link`) |
| `GET /orders/{id}/payments`, `GET /payments/{id}` | order participants |
| `POST /payments/{id}/utr` | buyer reports a bank reference: payment becomes `pending` and **never** `paid` |
| `POST /payments/webhooks/{provider}` | provider callback, verified by signature |
| `POST /dev/payments/{id}/simulate` | development + mock only: `{outcome: success\|failure\|pending, delay_seconds}` |

### Chat
| | |
|---|---|
| `POST /listings/{id}/conversations` | buyer opens (or gets) the conversation with the seller |
| `GET /conversations` | mine, with `unread` and `last_message` |
| `GET /conversations/unread-count` | total unread |
| `GET /conversations/{id}/messages` | `?before=<id>` (scroll back) or `?after=<id>` (catch up after reconnect) |
| `POST /conversations/{id}/messages` | `{client_msg_id, body}`; retrying with the same `client_msg_id` is idempotent |
| `POST /conversations/{id}/read` | `{last_read_message_id}`; read receipt to the other side |

### Admin (role re-checked against the database on every call)
| | |
|---|---|
| `GET /admin/payouts?status=pending\|paid_out\|all` | ledger entries with `releasable` (order completed) |
| `POST /admin/payouts/{id}/mark-paid` | `{reference}`; only after the buyer confirmed receipt |
| `GET /admin/payments?status=pending` | payments waiting for verification (UTR submitted) |
| `POST /admin/payments/{id}/mark-paid` / `mark-failed` | manual verification |
| `GET /admin/users` | unverified users |
| `POST /admin/users/{id}/verify` | `{verified: true\|false}` |
| `POST /admin/users/{id}/reset-payment-defaults` | unblock a user from bidding |
| `POST /admin/listings/{id}/remove` | `{reason}`; also allowed for auctions with bids |

### Payment flows in one paragraph each

**Fixed price.** Request → seller accepts → buyer picks UPI (gets a `upi://pay?pa=<seller>…`
link) or cash → seller confirms receipt → handover → buyer completes. The platform never
holds this money: no fee, no `payments` row, no ledger entry.

**Auction.** At `bid_ends_at` the highest eligible bidder gets an `awaiting_payment` order
with `payment_due_at` = now + 30 min. Fee and payout are computed and stored on the order
at that moment. The buyer creates a payment, and a verified webhook or an admin marks it
paid. That moves the order to `paid`, marks the item sold and writes a ledger entry
(gross / fee / payout, payout `pending`). After handover and completion, an admin pays
the seller and records the reference. If the winner doesn't pay in time, the order
expires, the buyer's `payment_defaults` goes up, and the next distinct bidder is offered
the item at their own best bid. If no bidders are left, the item goes back on sale.

---

## WebSocket protocol

`GET /v1/ws` upgrades the connection. Pass the token as `Authorization: Bearer …`, or
`?access_token=…` for browsers (it is redacted from logs). One connection carries
everything:

```jsonc
// server → client on connect
{"type":"hello","data":{"user_id":"…","server_time":"…","token_expires_at":"…"}}

// client → server (each optionally with "id" for correlation; replies are "ack" or "error" with the same id)
{"type":"listing.subscribe","id":"s1","data":{"listing_id":"…"}}      // ack carries the live auction state
{"type":"listing.unsubscribe","id":"s2","data":{"listing_id":"…"}}
{"type":"chat.send","id":"m1","data":{"conversation_id":"…","client_msg_id":"<uuid>","body":"hi"}}
{"type":"chat.read","id":"r1","data":{"conversation_id":"…","last_read_message_id":42}}
{"type":"ping","id":"p1"}                                             // → {"type":"pong","id":"p1"}

// server → client events
bid.placed, auction.closed            (on subscribed listings)
chat.message, chat.read, bid.outbid, auction.won, order.requested, order.updated,
payment.paid, payout.paid             (personal)
```

- Server pings every 25 s. A client that falls behind is disconnected.
- The connection closes with code **4001** when the access token expires: refresh the
  token and reconnect.
- **Reconnecting:** reconnect, re-send `listing.subscribe` (its ack brings you up to
  date), and fetch missed chat with `GET /conversations/{id}/messages?after=<last id>`.
  Every message is in Postgres; pub/sub only delivers live events.
- Events are published through Redis, so they reach clients connected to any instance.

---

## How the hard parts work

**Read routing.** `db.Router.Reader(ctx, ReplicaOK|Strong)` is the only place that
chooses a pool.
- Orders, payments, bids, availability and auth always use `Strong` (the primary).
- After a user commits a write, `WithTx` sets `recent_write:{userID}` for 10 s. That
  user's `ReplicaOK` reads then go to the primary, and they skip the feed cache, so they
  always see their own change.

**Bidding atomicity.**
1. Go checks eligibility against primary data.
2. `place.lua` validates and applies the bid in one Redis call: round, open, end time
   (by Redis `TIME`), not the seller, not already highest, `amount ≥ high + increment`.
   It also applies anti-sniping. Two concurrent bids can't both win.
3. Inside a transaction holding the listing's `FOR UPDATE` lock, the bid is inserted and
   checked again against Postgres's stored highest bid. Postgres is the source of truth.
4. If the write fails, `revert.lua` restores the previous state, but only if this bid is
   still on top. If Postgres saw a higher bid first, Redis is re-synced from Postgres.
5. On startup, Redis auction state is rebuilt from Postgres, and a missing key is
   reloaded lazily.

**Closing auctions.**
- Each `(round, end time)` gets a delayed asynq task, and an extension schedules a new one.
- The close job runs `close.lua` (stop bids, or reschedule if the auction was extended).
  Then, under the row lock, it stamps `auction_closed_at` and awards the round.
- Repeated or concurrent runs find the round already closed and do nothing. The
  one-live-order unique index backs this up. A 1-minute sweep catches lost tasks.

**One buyer per item.**
- Every order transition locks the listing row, then the order row (always in that
  order, so they can't deadlock).
- A partial unique index allows only one order per listing in
  `accepted…completed`.

**Feed cache.**
- Pages are cached per hostel under a version key. A write bumps the version (one
  `INCR`), and bumps it again 2 s later. That second bump discards pages a concurrent
  reader may have refilled from a replica that was still behind.
- The TTL is 30 s.

**Rate limiting.**
- GCRA (a token bucket) in one Lua call using Redis `TIME`, atomic across instances.
- If Redis is down, rate limiting fails open. Token revocation fails closed (503).

---

## Assumptions

These were agreed before building (the first ten) or decided during the build where the
spec was silent.

1. **Hostel scoping.** The feed shows the viewer's hostel only. Buying, bidding and
   chatting require the same hostel as the seller.
2. **Verification.** Unverified users can browse. Selling, buying, bidding, chatting and
   uploading need `is_verified`. Selling also needs a UPI ID, even for cash sales.
   College-domain auto-verification proves nothing about owning the address (no email
   confirmation yet).
3. **Fixed-price flow.**
   - Accepting a request reserves the item; other requests wait and are auto-rejected
     when the item sells.
   - The buyer picks UPI or cash (`awaiting_payment`); the seller confirms receipt
     (`paid`) and marks handover; the buyer completes.
   - Requests expire after 48 h. There's no payment deadline for fixed-price orders.
4. **`reserved` listing status.** It exists in addition to active/sold/removed and is
   hidden from the feed.
5. **UPI-link payments.** These can't be verified automatically. A buyer's UTR makes
   the payment `pending`, which holds the deadline for `PAYMENT_PENDING_GRACE`; an
   admin then marks it paid or failed. For gateways that can be queried, a pending
   payment triggers a `GetStatus` reconcile.
6. **Payouts.** An admin can mark a payout paid only after the order is `completed`.
   The ledger entry is written when the order is paid.
7. **"Flag the buyer".** This increments `payment_defaults`. At
   `MAX_PAYMENT_DEFAULTS` (2) the user can't bid until an admin resets it.
8. **Unsold auctions.** With no bids, or no eligible bidders left, the listing stays
   `active` with the round closed. The seller starts a new round by changing the bidding
   settings (or switches to fixed price). Re-offers go to the next distinct
   non-defaulted bidder at their own best bid.
9. **Who can remove an auction, and bid rules.** Once an auction has a bid, only an
   admin can remove it. The current top bidder can't raise their own bid. The first bid
   may equal the starting price.
10. **Anti-sniping.** A bid within the last 30 s sets `end = end + 60 s`, with no cap
    on extensions.
11. **Hostel and block names.** Both are canonicalised (trimmed, single-spaced,
    upper-case) so eligibility comparisons are exact. Hostel can't be changed through
    the API. `year` means year of study (1–6).
12. **Money limits.** Each amount is capped at ₹1 crore (`1e9` paise). The fee percent
    accepts up to 2 decimals and is stored in basis points on the order.
13. **Auction length and the time window.**
    - An auction must last between 10 minutes and 14 days.
    - The time window is minute-precise in IST: the start is included, the end is
      excluded, from > until crosses midnight, and from == until is rejected.
14. **When eligibility is checked.** It is enforced when a purchase request is created
    and on every bid, but not re-checked when the seller accepts or when payment
    happens.
15. **Bid ordering.** A bid that Redis accepted can still be refused with `409 outbid`
    if a higher concurrent bid reached Postgres first. That keeps the stored history
    strictly increasing.
16. **Rate-limit keys.** Login is limited per email+IP and registration per IP (default
    20/hour, because a hostel often shares one NAT address). The global limit is per
    user, or per IP for anonymous requests.
17. **Refresh tokens.** Clients must not refresh concurrently with the same refresh
    token. The second request looks like token reuse and revokes that login chain.
18. **Search and sorting.**
    - Full-text search uses the `english` configuration.
    - The feed sorts by newest or price, with no relevance ranking.
    - The auction price shown in the feed is the stored value, which can trail the live
      Redis value by moments. The detail page and the auction endpoint read Redis.
19. **Chat.** A buyer opens a conversation (verified, same hostel); sellers reply.
    Messages are read from the primary so reconnect catch-up never misses one.
20. **Idempotency keys.** Payments are idempotent per buyer per key. To retry after a
    failed payment, use a new key.
21. **Late payments.** Money that arrives for an expired order is recorded as paid with a
    "refund required" note. There's no automated refund flow.
22. **Admin removal.** An admin can't remove a listing that has a live order; resolve
    the order first.
23. **First admin.** It is created with SQL. Periodic sweeps run on every instance; they
    are idempotent and `Unique`-guarded.

---

## Known limitations and next steps

- **Integration tests haven't run yet.** They were written without a local Postgres or
  Redis available, so they skipped. Set `TEST_DATABASE_URL` / `TEST_REDIS_URL` and run
  `make test` before relying on them. Unit tests, `go vet` and golangci-lint all pass.
  sqlc parsed every migration and query with the real Postgres parser.
- **Real payment gateway.** Implement `payments.Provider` for Razorpay or Cashfree and
  set `PAYMENT_PROVIDER`.
  - Some UPI apps restrict intents to personal (non-merchant) UPI IDs, which affects
    fixed-price direct payments.
  - Collecting money on behalf of sellers in India normally requires a licensed payment
    aggregator (RBI guidelines).
- No refunds or disputes flow, no email or push notifications (WebSocket events only),
  and no cleanup of Cloudinary uploads that were never attached to a listing.
