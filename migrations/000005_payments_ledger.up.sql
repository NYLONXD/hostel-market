-- Only platform-collected payments (auction orders) live here. Fixed-price orders are paid
-- buyer -> seller directly and never touch this table.
CREATE TABLE payments (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id        uuid NOT NULL REFERENCES orders (id),
  payer_id        uuid NOT NULL REFERENCES users (id),
  provider        text NOT NULL CHECK (provider IN ('upi_link', 'mock')),
  provider_ref    text,
  amount_paise    bigint NOT NULL CHECK (amount_paise > 0),
  currency        text NOT NULL DEFAULT 'INR',
  status          text NOT NULL CHECK (status IN ('created', 'pending', 'paid', 'failed', 'expired')),
  idempotency_key text NOT NULL,
  upi_uri         text,
  buyer_utr       text,                                     -- informational only; never marks paid
  failure_reason  text,
  verified_by     uuid REFERENCES users (id),               -- set when an admin marks it paid
  paid_at         timestamptz,
  expires_at      timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (payer_id, idempotency_key),
  UNIQUE (provider, provider_ref)
);

CREATE UNIQUE INDEX payments_one_live_per_order ON payments (order_id)
  WHERE status IN ('created', 'pending', 'paid');

CREATE TRIGGER payments_set_updated_at BEFORE UPDATE ON payments
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Every verified webhook is recorded once; replays hit the unique key and are ignored.
CREATE TABLE payment_webhook_events (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  provider     text NOT NULL,
  event_id     text NOT NULL,
  payload      jsonb NOT NULL,
  received_at  timestamptz NOT NULL DEFAULT now(),
  processed_at timestamptz,
  UNIQUE (provider, event_id)
);

CREATE TABLE ledger_entries (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id            uuid NOT NULL UNIQUE REFERENCES orders (id),
  payment_id          uuid NOT NULL UNIQUE REFERENCES payments (id),
  seller_id           uuid NOT NULL REFERENCES users (id),
  gross_paise         bigint NOT NULL CHECK (gross_paise > 0),
  platform_fee_paise  bigint NOT NULL CHECK (platform_fee_paise >= 0),
  seller_payout_paise bigint NOT NULL CHECK (seller_payout_paise >= 0),
  payout_status       text NOT NULL DEFAULT 'pending' CHECK (payout_status IN ('pending', 'paid_out')),
  payout_reference    text,
  paid_out_at         timestamptz,
  paid_out_by         uuid REFERENCES users (id),
  created_at          timestamptz NOT NULL DEFAULT now(),
  CHECK (gross_paise = platform_fee_paise + seller_payout_paise),
  CHECK ((payout_status = 'paid_out') = (payout_reference IS NOT NULL AND paid_out_at IS NOT NULL))
);

CREATE INDEX ledger_pending_payouts ON ledger_entries (created_at) WHERE payout_status = 'pending';
CREATE INDEX ledger_seller ON ledger_entries (seller_id, created_at DESC);
