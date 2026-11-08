-- =============================================================================
-- Hostel Market - 01_schema.sql
-- Tables, keys, constraints and indexes. PostgreSQL 14+.
--
-- Normalisation notes (see docs/DATABASE.md):
--   * A user's hostel is NOT stored on the user: user -> block -> hostel.
--     Storing it would create the transitive dependency user_id -> block_id -> hostel_id
--     (a 3NF violation), so a listing's hostel is found through its seller.
--   * An order does NOT store seller_id: it is determined by listing_id.
--   * orders.price IS stored: it is the price agreed when the request was made,
--     a historical fact that must not change when the seller edits the listing.
-- =============================================================================

-- ---------------------------------------------------------------------------
-- HOSTELS and BLOCKS (master data, managed by admins)
-- ---------------------------------------------------------------------------
CREATE TABLE hostels (
  hostel_id  integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name       varchar(100) NOT NULL UNIQUE CHECK (length(trim(name)) > 0),
  created_at timestamptz  NOT NULL DEFAULT now()
);

CREATE TABLE blocks (
  block_id  integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  hostel_id integer     NOT NULL REFERENCES hostels (hostel_id) ON DELETE RESTRICT,
  name      varchar(20) NOT NULL CHECK (length(trim(name)) > 0),
  CONSTRAINT blocks_unique_name_per_hostel UNIQUE (hostel_id, name)
);

-- ---------------------------------------------------------------------------
-- USERS
-- ---------------------------------------------------------------------------
CREATE TABLE users (
  user_id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  email         varchar(254) NOT NULL UNIQUE
                  CHECK (email = lower(email) AND email LIKE '%_@_%._%'),
  password_hash text         NOT NULL,
  full_name     varchar(100) NOT NULL CHECK (length(trim(full_name)) > 0),
  block_id      integer      NOT NULL REFERENCES blocks (block_id) ON DELETE RESTRICT,
  year_of_study smallint     NOT NULL CHECK (year_of_study BETWEEN 1 AND 6),
  upi_id        varchar(100) CHECK (upi_id ~ '^[a-zA-Z0-9._-]{2,256}@[a-zA-Z]{2,64}$'),
  role          varchar(10)  NOT NULL DEFAULT 'student' CHECK (role IN ('student', 'admin')),
  is_verified   boolean      NOT NULL DEFAULT false,
  verified_at   timestamptz,
  created_at    timestamptz  NOT NULL DEFAULT now(),
  updated_at    timestamptz  NOT NULL DEFAULT now(),
  CONSTRAINT users_verified_pair CHECK (is_verified = (verified_at IS NOT NULL))
);

CREATE INDEX users_block ON users (block_id);
CREATE INDEX users_unverified ON users (created_at) WHERE NOT is_verified;

-- ---------------------------------------------------------------------------
-- CATEGORIES
-- ---------------------------------------------------------------------------
CREATE TABLE categories (
  category_id smallint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  slug        varchar(30) NOT NULL UNIQUE CHECK (slug ~ '^[a-z_]+$'),
  name        varchar(50) NOT NULL UNIQUE
);

-- ---------------------------------------------------------------------------
-- LISTINGS (items for sale; prices are whole rupees)
-- ---------------------------------------------------------------------------
CREATE TABLE listings (
  listing_id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  seller_id        bigint        NOT NULL REFERENCES users (user_id) ON DELETE RESTRICT,
  category_id      smallint      NOT NULL REFERENCES categories (category_id) ON DELETE RESTRICT,
  title            varchar(120)  NOT NULL CHECK (length(trim(title)) >= 3),
  description      varchar(4000) NOT NULL DEFAULT '',
  item_condition   varchar(10)   NOT NULL
                     CHECK (item_condition IN ('new', 'like_new', 'good', 'fair', 'poor')),
  price            integer       NOT NULL CHECK (price BETWEEN 1 AND 1000000),
  status           varchar(10)   NOT NULL DEFAULT 'active'
                     CHECK (status IN ('active', 'reserved', 'sold', 'removed')),

  -- Optional buyer-eligibility rules set by the seller.
  -- Times are Asia/Kolkata wall-clock; from > until means the window crosses midnight.
  available_from   time,
  available_until  time,
  allowed_block_id integer REFERENCES blocks (block_id) ON DELETE SET NULL,
  allowed_year     smallint CHECK (allowed_year BETWEEN 1 AND 6),

  -- Full-text search document, kept up to date by PostgreSQL itself.
  search_doc       tsvector GENERATED ALWAYS AS (
                     setweight(to_tsvector('english', title), 'A') ||
                     setweight(to_tsvector('english', description), 'B')) STORED,

  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  removed_at       timestamptz,

  CONSTRAINT listings_window_pair CHECK (
    (available_from IS NULL) = (available_until IS NULL)
    AND (available_from IS NULL OR available_from <> available_until)),
  CONSTRAINT listings_removed_at CHECK ((status = 'removed') = (removed_at IS NOT NULL))
);

CREATE INDEX listings_seller       ON listings (seller_id, created_at DESC);
CREATE INDEX listings_active_new   ON listings (created_at DESC) WHERE status = 'active';
CREATE INDEX listings_active_price ON listings (price) WHERE status = 'active';
CREATE INDEX listings_category     ON listings (category_id) WHERE status = 'active';
CREATE INDEX listings_search       ON listings USING gin (search_doc);

CREATE TABLE listing_photos (
  photo_id    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  listing_id  bigint   NOT NULL REFERENCES listings (listing_id) ON DELETE CASCADE,
  url         text     NOT NULL,
  storage_key text     NOT NULL UNIQUE,      -- Cloudinary public_id or local file name
  position    smallint NOT NULL CHECK (position BETWEEN 0 AND 4),   -- at most 5 photos
  created_at  timestamptz NOT NULL DEFAULT now(),
  -- Deferred so photos can be re-numbered inside one transaction after a delete.
  CONSTRAINT listing_photos_position UNIQUE (listing_id, position) DEFERRABLE INITIALLY DEFERRED
);

-- ---------------------------------------------------------------------------
-- ORDERS (buy requests and their life cycle)
--
--   requested -> accepted -> awaiting_payment -> paid -> handed_over -> completed
--   requested -> rejected | cancelled | expired
--   accepted / awaiting_payment -> cancelled
-- ---------------------------------------------------------------------------
CREATE TABLE orders (
  order_id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  listing_id         bigint      NOT NULL REFERENCES listings (listing_id) ON DELETE RESTRICT,
  buyer_id           bigint      NOT NULL REFERENCES users (user_id) ON DELETE RESTRICT,
  price              integer     NOT NULL CHECK (price > 0),        -- snapshot at request time
  status             varchar(20) NOT NULL DEFAULT 'requested'
                       CHECK (status IN ('requested', 'accepted', 'awaiting_payment', 'paid',
                                         'handed_over', 'completed', 'rejected', 'cancelled', 'expired')),
  payment_method     varchar(10) CHECK (payment_method IN ('upi', 'cash')),
  buyer_note         varchar(500),
  request_expires_at timestamptz NOT NULL,
  accepted_at        timestamptz,
  paid_at            timestamptz,
  handed_over_at     timestamptz,
  completed_at       timestamptz,
  closed_at          timestamptz,                 -- when rejected / cancelled / expired
  close_reason       varchar(200),
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT orders_method_when_paying CHECK (
    status NOT IN ('awaiting_payment', 'paid', 'handed_over', 'completed') OR payment_method IS NOT NULL),
  CONSTRAINT orders_closed_at CHECK (
    (status IN ('rejected', 'cancelled', 'expired')) = (closed_at IS NOT NULL))
);

-- At most ONE order per listing can be past the request stage: two buyers can never
-- both get the same item, even if two requests are accepted at the same moment.
CREATE UNIQUE INDEX orders_one_live_per_listing ON orders (listing_id)
  WHERE status IN ('accepted', 'awaiting_payment', 'paid', 'handed_over', 'completed');
-- A buyer can have only one pending request per listing.
CREATE UNIQUE INDEX orders_one_request_per_buyer ON orders (listing_id, buyer_id)
  WHERE status = 'requested';
CREATE INDEX orders_buyer   ON orders (buyer_id, created_at DESC);
CREATE INDEX orders_listing ON orders (listing_id);
CREATE INDEX orders_request_expiry ON orders (request_expires_at) WHERE status = 'requested';

-- Audit trail, filled automatically by a trigger on orders.
CREATE TABLE order_status_history (
  history_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id   bigint      NOT NULL REFERENCES orders (order_id) ON DELETE CASCADE,
  old_status varchar(20),                         -- NULL for the creation row
  new_status varchar(20) NOT NULL,
  changed_by bigint      REFERENCES users (user_id) ON DELETE SET NULL,  -- NULL = system
  note       varchar(200),
  changed_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX order_status_history_order ON order_status_history (order_id, history_id);

-- ---------------------------------------------------------------------------
-- CHAT: one conversation per (listing, buyer); the seller comes from the listing.
-- ---------------------------------------------------------------------------
CREATE TABLE conversations (
  conversation_id     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  listing_id          bigint NOT NULL REFERENCES listings (listing_id) ON DELETE CASCADE,
  buyer_id            bigint NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
  buyer_last_read_id  bigint NOT NULL DEFAULT 0,
  seller_last_read_id bigint NOT NULL DEFAULT 0,
  last_message_at     timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT conversations_one_per_buyer UNIQUE (listing_id, buyer_id)
);

CREATE INDEX conversations_buyer ON conversations (buyer_id);

CREATE TABLE messages (
  message_id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  conversation_id bigint        NOT NULL REFERENCES conversations (conversation_id) ON DELETE CASCADE,
  sender_id       bigint        NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
  body            varchar(2000) NOT NULL CHECK (length(trim(body)) > 0),
  created_at      timestamptz   NOT NULL DEFAULT now()
);

CREATE INDEX messages_conversation ON messages (conversation_id, message_id);
