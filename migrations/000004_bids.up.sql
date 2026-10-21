CREATE TABLE bids (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  listing_id    uuid NOT NULL REFERENCES listings (id),
  auction_round int NOT NULL,
  bidder_id     uuid NOT NULL REFERENCES users (id),
  amount_paise  bigint NOT NULL CHECK (amount_paise > 0),
  created_at    timestamptz NOT NULL DEFAULT now(),
  -- Accepted bids strictly increase, so equal amounts in one round mean a bug; refuse them.
  UNIQUE (listing_id, auction_round, amount_paise)
);

CREATE INDEX bids_listing_round ON bids (listing_id, auction_round, amount_paise DESC);
CREATE INDEX bids_bidder ON bids (bidder_id, created_at DESC);

ALTER TABLE orders ADD COLUMN winning_bid_id bigint REFERENCES bids (id);
