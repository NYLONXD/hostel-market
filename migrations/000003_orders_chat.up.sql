CREATE TABLE orders (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  listing_id          uuid NOT NULL REFERENCES listings (id),
  buyer_id            uuid NOT NULL REFERENCES users (id),
  seller_id           uuid NOT NULL REFERENCES users (id),
  kind                text NOT NULL CHECK (kind IN ('fixed', 'auction')),
  status              text NOT NULL CHECK (status IN ('requested', 'accepted', 'awaiting_payment', 'paid',
                        'handed_over', 'completed', 'rejected', 'cancelled', 'expired')),

  -- Money is snapshotted at creation so later config changes never alter old orders.
  price_paise         bigint NOT NULL CHECK (price_paise > 0),
  platform_fee_bps    int NOT NULL DEFAULT 0 CHECK (platform_fee_bps BETWEEN 0 AND 10000),
  platform_fee_paise  bigint NOT NULL DEFAULT 0 CHECK (platform_fee_paise >= 0),
  seller_payout_paise bigint NOT NULL CHECK (seller_payout_paise >= 0),

  payment_method      text CHECK (payment_method IN ('upi_direct', 'cash', 'platform')),
  auction_round       int,
  buyer_note          text CHECK (char_length(buyer_note) <= 500),
  request_expires_at  timestamptz,
  payment_due_at      timestamptz,

  accepted_at         timestamptz,
  paid_at             timestamptz,
  handed_over_at      timestamptz,
  completed_at        timestamptz,
  closed_at           timestamptz,                          -- when rejected / cancelled / expired
  close_reason        text,

  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),

  CHECK (buyer_id <> seller_id),
  CHECK (platform_fee_paise + seller_payout_paise = price_paise),
  CHECK (kind = 'auction' OR (platform_fee_bps = 0 AND platform_fee_paise = 0))
);

-- At most one order per listing can be past the request stage. This backs up the
-- SELECT ... FOR UPDATE lock on the listing row.
CREATE UNIQUE INDEX orders_one_live_per_listing ON orders (listing_id)
  WHERE status IN ('accepted', 'awaiting_payment', 'paid', 'handed_over', 'completed');
CREATE UNIQUE INDEX orders_one_request_per_buyer ON orders (listing_id, buyer_id) WHERE status = 'requested';
CREATE INDEX orders_buyer ON orders (buyer_id, created_at DESC);
CREATE INDEX orders_seller ON orders (seller_id, created_at DESC);
CREATE INDEX orders_payment_due ON orders (payment_due_at) WHERE status = 'awaiting_payment' AND kind = 'auction';
CREATE INDEX orders_request_expiry ON orders (request_expires_at) WHERE status = 'requested';

CREATE TRIGGER orders_set_updated_at BEFORE UPDATE ON orders
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE conversations (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  listing_id          uuid NOT NULL REFERENCES listings (id),
  buyer_id            uuid NOT NULL REFERENCES users (id),
  seller_id           uuid NOT NULL REFERENCES users (id),
  buyer_last_read_id  bigint NOT NULL DEFAULT 0,
  seller_last_read_id bigint NOT NULL DEFAULT 0,
  last_message_at     timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (listing_id, buyer_id),
  CHECK (buyer_id <> seller_id)
);

CREATE INDEX conversations_buyer ON conversations (buyer_id, last_message_at DESC NULLS LAST);
CREATE INDEX conversations_seller ON conversations (seller_id, last_message_at DESC NULLS LAST);

CREATE TABLE messages (
  id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  conversation_id uuid NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
  sender_id       uuid NOT NULL REFERENCES users (id),
  client_msg_id   uuid NOT NULL,                            -- makes client retries idempotent
  body            text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 2000),
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (conversation_id, sender_id, client_msg_id)
);

CREATE INDEX messages_conversation ON messages (conversation_id, id);
