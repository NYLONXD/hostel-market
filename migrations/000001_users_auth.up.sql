CREATE EXTENSION IF NOT EXISTS citext;

-- Shared trigger: keeps updated_at honest without every query having to set it.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE users (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email            citext NOT NULL UNIQUE,
  password_hash    text NOT NULL,
  name             text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  hostel           text NOT NULL CHECK (char_length(hostel) BETWEEN 1 AND 100),
  block            text NOT NULL CHECK (char_length(block) BETWEEN 1 AND 20),   -- stored upper-cased
  year             smallint NOT NULL CHECK (year BETWEEN 1 AND 6),             -- year of study
  upi_id           text CHECK (upi_id ~ '^[a-zA-Z0-9._-]{2,256}@[a-zA-Z]{2,64}$'),
  role             text NOT NULL DEFAULT 'student' CHECK (role IN ('student', 'admin')),
  is_verified      boolean NOT NULL DEFAULT false,
  verified_at      timestamptz,
  payment_defaults int NOT NULL DEFAULT 0 CHECK (payment_defaults >= 0),        -- unpaid auction wins
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX users_unverified ON users (created_at) WHERE NOT is_verified;

CREATE TRIGGER users_set_updated_at BEFORE UPDATE ON users
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One row per issued refresh token. Rotation marks the old row used; presenting a
-- used token again is treated as theft and revokes the whole family.
CREATE TABLE refresh_tokens (
  jti        uuid PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  family_id  uuid NOT NULL,
  expires_at timestamptz NOT NULL,
  used_at    timestamptz,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX refresh_tokens_family ON refresh_tokens (family_id);
CREATE INDEX refresh_tokens_user ON refresh_tokens (user_id);
