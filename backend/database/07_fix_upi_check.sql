-- =============================================================================
-- Midnight Munchies - 07_fix_upi_check.sql
-- Fixes the UPI id CHECK constraint on users.
--
-- Bug: the pattern used {2,256}, but PostgreSQL regexes allow a repetition count
-- of at most 255. The constraint only fails when upi_id is not NULL, so every
-- save of a UPI id raised "invalid regular expression: invalid repetition
-- count(s)" (SQLSTATE 2201B) and the API returned 500.
-- Safe to run more than once.
-- =============================================================================

BEGIN;

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_upi_id_check;
ALTER TABLE users ADD CONSTRAINT users_upi_id_check
  CHECK (upi_id ~ '^[a-zA-Z0-9._-]{2,255}@[a-zA-Z]{2,64}$');

COMMIT;

-- Check: should print true
SELECT 'name.123@okaxis' ~ '^[a-zA-Z0-9._-]{2,255}@[a-zA-Z]{2,64}$' AS upi_pattern_ok;
