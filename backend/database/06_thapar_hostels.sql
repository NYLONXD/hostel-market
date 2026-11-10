-- =============================================================================
-- Midnight Munchies - 06_thapar_hostels.sql
-- Adds the TIET (Thapar, Patiala) hostels and late-night snack categories.
-- Safe to run more than once (ON CONFLICT DO NOTHING), and safe on a database
-- that already has data: nothing is deleted or renamed.
--
-- The schema has no gender column, so "Boys"/"Girls" is part of the name.
-- Hall names are given only where they were confirmed; rename any hostel later:
--   UPDATE hostels SET name = 'Hostel H - <Hall name> (Boys)' WHERE name = 'Hostel H (Boys)';
-- =============================================================================

BEGIN;

-- ---------------------------------------------------------------------------
-- Hostels
-- ---------------------------------------------------------------------------
INSERT INTO hostels (name) VALUES
  -- Boys
  ('Hostel A - Agira Hall (Boys)'),
  ('Hostel B - Amritam Hall (Boys)'),
  ('Hostel C - Prithvi Hall (Boys)'),
  ('Hostel D (Boys)'),
  ('Hostel H (Boys)'),
  ('Hostel J (Boys)'),
  ('Hostel K (Boys)'),
  ('Hostel L (Boys)'),
  ('Hostel M (Boys)'),
  -- Girls
  ('Hostel E - Vasudha Hall (Girls)'),
  ('Hostel G (Girls)'),
  ('Hostel I (Girls)'),
  ('Hostel N (Girls)'),
  ('Hostel PG (Girls)')
ON CONFLICT (name) DO NOTHING;

-- Every user must belong to a block (users.block_id is NOT NULL), so each new
-- hostel gets one default block. Admins can add real wings/blocks from /admin.
INSERT INTO blocks (hostel_id, name)
SELECT h.hostel_id, 'Main'
FROM hostels h
WHERE h.name LIKE 'Hostel %(Boys)' OR h.name LIKE 'Hostel %(Girls)'
ON CONFLICT ON CONSTRAINT blocks_unique_name_per_hostel DO NOTHING;

-- ---------------------------------------------------------------------------
-- Late-night snack categories
-- ---------------------------------------------------------------------------
INSERT INTO categories (slug, name) VALUES
  ('maggi_noodles', 'Maggi & Noodles'),
  ('chips_namkeen', 'Chips & Namkeen'),
  ('biscuits',      'Biscuits & Cookies'),
  ('chocolates',    'Chocolates & Sweets'),
  ('cold_drinks',   'Cold Drinks & Juices'),
  ('tea_coffee',    'Tea & Coffee'),
  ('homemade',      'Homemade Food'),
  ('ready_to_eat',  'Ready to Eat')
ON CONFLICT DO NOTHING;

COMMIT;

-- Check the result
SELECT h.hostel_id, h.name AS hostel, string_agg(b.name, ', ') AS blocks
FROM hostels h
LEFT JOIN blocks b ON b.hostel_id = h.hostel_id
GROUP BY h.hostel_id, h.name
ORDER BY h.name;
