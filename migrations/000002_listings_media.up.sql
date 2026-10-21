CREATE TABLE listings (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  seller_id            uuid NOT NULL REFERENCES users (id),
  hostel               text NOT NULL,                       -- copied from the seller at creation
  title                text NOT NULL CHECK (char_length(title) BETWEEN 3 AND 120),
  description          text NOT NULL DEFAULT '' CHECK (char_length(description) <= 4000),
  category             text NOT NULL CHECK (category IN ('books', 'cycles', 'electronics', 'appliances',
                         'furniture', 'clothing', 'sports', 'stationery', 'other')),
  condition            text NOT NULL CHECK (condition IN ('new', 'like_new', 'good', 'fair', 'poor')),
  status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'reserved', 'sold', 'removed')),

  bidding_enabled      boolean NOT NULL DEFAULT false,
  price_paise          bigint CHECK (price_paise > 0),
  starting_price_paise bigint CHECK (starting_price_paise > 0),
  min_increment_paise  bigint CHECK (min_increment_paise > 0),
  bid_ends_at          timestamptz,
  auction_round        int NOT NULL DEFAULT 1 CHECK (auction_round >= 1),
  auction_closed_at    timestamptz,                         -- idempotency marker for the close job
  first_bid_at         timestamptz,                         -- non-null => bidding settings are locked
  current_bid_paise    bigint CHECK (current_bid_paise > 0),
  bid_count            int NOT NULL DEFAULT 0 CHECK (bid_count >= 0),
  display_price_paise  bigint GENERATED ALWAYS AS
                         (COALESCE(current_bid_paise, price_paise, starting_price_paise)) STORED,

  -- Eligibility. Times are Asia/Kolkata wall-clock; from > until means the window crosses midnight.
  available_from       time,
  available_until      time,
  allowed_block        text,
  allowed_year         smallint CHECK (allowed_year BETWEEN 1 AND 6),

  search_tsv           tsvector GENERATED ALWAYS AS (
                         setweight(to_tsvector('english', title), 'A') ||
                         setweight(to_tsvector('english', description), 'B')) STORED,

  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  removed_at           timestamptz,

  CONSTRAINT listings_price_mode CHECK (
    (NOT bidding_enabled AND price_paise IS NOT NULL AND starting_price_paise IS NULL
       AND min_increment_paise IS NULL AND bid_ends_at IS NULL)
    OR
    (bidding_enabled AND price_paise IS NULL AND starting_price_paise IS NOT NULL
       AND min_increment_paise IS NOT NULL AND bid_ends_at IS NOT NULL)),
  CONSTRAINT listings_window_pair CHECK (
    (available_from IS NULL) = (available_until IS NULL)
    AND (available_from IS NULL OR available_from <> available_until))
);

CREATE INDEX listings_feed_new ON listings (hostel, created_at DESC, id DESC) WHERE status = 'active';
CREATE INDEX listings_feed_category ON listings (hostel, category, created_at DESC, id DESC) WHERE status = 'active';
CREATE INDEX listings_feed_price ON listings (hostel, display_price_paise, id) WHERE status = 'active';
CREATE INDEX listings_search ON listings USING gin (search_tsv);
CREATE INDEX listings_seller ON listings (seller_id, created_at DESC);
CREATE INDEX listings_open_auctions ON listings (bid_ends_at)
  WHERE bidding_enabled AND status = 'active' AND auction_closed_at IS NULL;

CREATE TRIGGER listings_set_updated_at BEFORE UPDATE ON listings
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE listing_photos (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  listing_id uuid NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
  public_id  text NOT NULL UNIQUE,                          -- an asset can't be attached to two listings
  secure_url text NOT NULL,
  width      int,
  height     int,
  bytes      int,
  format     text,
  position   smallint NOT NULL CHECK (position BETWEEN 0 AND 4),   -- max 5 photos per listing
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (listing_id, position)
);
