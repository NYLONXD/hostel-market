# Hostel Market

A marketplace where students buy and sell used items (books, cycles, appliances…) **inside
their own hostel**. Built as a DBMS mini project: the business rules live in PostgreSQL
(constraints, triggers, stored functions, procedures and views), with a thin FastAPI layer
and a Next.js web app on top.

```
backend/     FastAPI (Python 3.12+) + raw SQL via psycopg 3
  app/         routers, auth, db helpers, optional Redis cache, photo storage
  database/    00_reset … 05_sample_queries.sql: everything SQL, in run order
  scripts/     init_db.py (create schema / seed), make_admin.py
  tests/       unit tests + integration tests against a real PostgreSQL
frontend/    Next.js 16 (App Router, TypeScript, Tailwind CSS 4)
docs/        DATABASE.md: ER diagram, schema, normalization, constraints (report chapter 4-5)
```

## Features

- Sign up with hostel, block and year; college-email sign-ups are verified automatically,
  the rest by an admin. Browsing is open to everyone; buying, selling and chat need verification.
- Listings with up to 5 photos, categories, condition, full-text search, filters and sorting.
  The feed only shows the viewer's own hostel.
- Optional seller rules: only one block, only one year of study, only during certain hours (IST).
- Buy requests and an order life cycle: request → accept → choose UPI/cash → seller confirms
  payment → handover → buyer confirms. One buyer per item; others wait on a waitlist; requests
  expire after 48 h. Every status change is kept in an audit log.
- Buyer-seller chat with unread counts and read receipts (the web app polls every few seconds).
- Admin dashboard: statistics from views, verify users, remove listings, add hostels, blocks and categories.

Removed from the earlier Go version: auctions/bidding, platform payments (gateway, ledger,
payouts), WebSockets, read replicas, the Redis job queue and rate limiting.

## Storage

| Store | Used for |
|---|---|
| **PostgreSQL** (hosted) | all data, the source of truth |
| **Redis** (optional) | short-lived cache of feed pages and the hostel/category lists. Leave `REDIS_URL` empty to turn it off |
| **Browser localStorage** | per-device conveniences: session token, feed filters, unfinished listing draft, recently viewed items |
| **Cloudinary** (optional) | listing photos. Without `CLOUDINARY_URL` photos are saved in `backend/uploads/` |

## Setup

### 1. Database

Create a PostgreSQL database on any host (Neon, Supabase, Render, Railway…) and copy its
connection string. Keep `?sslmode=require`.

### 2. Backend

```powershell
cd backend
python -m venv .venv
.venv\Scripts\activate            # macOS/Linux: source .venv/bin/activate
pip install -r requirements.txt
Copy-Item .env.example .env       # then set DATABASE_URL and JWT_SECRET
python -m scripts.init_db --seed  # creates tables, functions, views + demo data
uvicorn app.main:app --reload     # http://localhost:8000/docs
```

Demo accounts (password `password123`): `admin@college.edu` (admin), `aman@college.edu`,
`riya@college.edu`, `kabir@college.edu` (Aryabhatta Hostel), `sneha@college.edu` (Kalpana
Chawla Hostel), `rohan@gmail.com` (unverified).

To start over: `python -m scripts.init_db --reset --seed` (asks before deleting data).
To make your own account an admin: `python -m scripts.make_admin you@college.edu`.

### 3. Frontend

```powershell
cd frontend
npm install
Copy-Item .env.example .env.local   # NEXT_PUBLIC_API_URL=http://localhost:8000/api
npm run dev                         # http://localhost:3000
```

### Tests

```powershell
cd backend
pytest                       # unit tests only
# integration tests: set TEST_DATABASE_URL in .env to a SEPARATE, empty database
# (the suite drops and recreates all tables there), then run pytest again.
```

## API overview

All routes are under `/api`; interactive docs at `/docs`. Errors look like
`{"error": {"code": "listing_reserved", "message": "…"}}`.

| Area | Endpoints |
|---|---|
| Auth | `POST /auth/register`, `POST /auth/login` (JWT bearer token) |
| Lookup | `GET /meta/hostels`, `GET /meta/categories` |
| Users | `GET/PATCH /users/me`, `GET /users/{id}`, `GET /users/me/listings` |
| Listings | `GET/POST /listings`, `GET/PATCH/DELETE /listings/{id}`, `POST /listings/{id}/photos`, `DELETE /listings/{id}/photos/{photo_id}` |
| Orders | `POST /listings/{id}/orders`, `GET /orders`, `GET /orders/{id}`, `POST /orders/{id}/{accept\|reject\|cancel\|choose_payment\|confirm_payment\|handover\|complete}` |
| Chat | `POST /listings/{id}/conversations`, `GET /conversations`, `GET /conversations/unread-count`, `GET/POST /conversations/{id}/messages`, `POST /conversations/{id}/read` |
| Admin | `GET /admin/stats`, `GET /admin/users`, `POST /admin/users/{id}/verify`, `GET /admin/listings`, `POST /admin/listings/{id}/remove`, `POST /admin/hostels`, `POST /admin/hostels/{id}/blocks`, `POST /admin/categories` |
| Health | `GET /health` |

## Code map (graphify)

The repo can be explored as a knowledge graph with [graphify](https://github.com/safishamsi/graphify):
`graphify update .` rebuilds `graphify-out/` (git-ignored) without any API calls, and `/graphify`
inside Claude Code adds docs and images to it.
