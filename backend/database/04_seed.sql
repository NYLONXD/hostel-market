-- =============================================================================
-- Hostel Market - 04_seed.sql
-- Sample data for demos and report screenshots. Run on an EMPTY database right
-- after 01-03 (ids below assume a fresh database).
-- Every demo account's password is:  password123
-- =============================================================================

INSERT INTO hostels (name) VALUES
  ('Aryabhatta Hostel'),          -- hostel_id 1
  ('Kalpana Chawla Hostel');      -- hostel_id 2

INSERT INTO blocks (hostel_id, name) VALUES
  (1, 'A'), (1, 'B'), (1, 'C'),   -- block_id 1, 2, 3
  (2, 'A'), (2, 'B');             -- block_id 4, 5

INSERT INTO categories (slug, name) VALUES
  ('books', 'Books'), ('cycles', 'Cycles'), ('electronics', 'Electronics'),
  ('appliances', 'Appliances'), ('furniture', 'Furniture'), ('clothing', 'Clothing'),
  ('sports', 'Sports'), ('stationery', 'Stationery'), ('other', 'Other');

INSERT INTO users (email, password_hash, full_name, block_id, year_of_study, upi_id, role, is_verified, verified_at) VALUES
  ('admin@college.edu', '$2b$10$Zf90C6gaE6WJ6ZV/aVaiVut9DjNpvM4yugIH8wkbRjoTAOfCntL9C', 'Hostel Admin', 1, 4, NULL,           'admin',   true,  now()),  -- 1
  ('aman@college.edu',  '$2b$10$Zf90C6gaE6WJ6ZV/aVaiVut9DjNpvM4yugIH8wkbRjoTAOfCntL9C', 'Aman Verma',   1, 2, 'aman@okaxis',  'student', true,  now()),  -- 2
  ('riya@college.edu',  '$2b$10$Zf90C6gaE6WJ6ZV/aVaiVut9DjNpvM4yugIH8wkbRjoTAOfCntL9C', 'Riya Sharma',  2, 3, 'riya@oksbi',   'student', true,  now()),  -- 3
  ('kabir@college.edu', '$2b$10$Zf90C6gaE6WJ6ZV/aVaiVut9DjNpvM4yugIH8wkbRjoTAOfCntL9C', 'Kabir Singh',  1, 1, NULL,           'student', true,  now()),  -- 4
  ('sneha@college.edu', '$2b$10$Zf90C6gaE6WJ6ZV/aVaiVut9DjNpvM4yugIH8wkbRjoTAOfCntL9C', 'Sneha Iyer',   4, 2, 'sneha@okicici','student', true,  now()),  -- 5
  ('rohan@gmail.com',   '$2b$10$Zf90C6gaE6WJ6ZV/aVaiVut9DjNpvM4yugIH8wkbRjoTAOfCntL9C', 'Rohan Das',    2, 1, NULL,           'student', false, NULL);   -- 6 (unverified)

INSERT INTO listings (seller_id, category_id, title, description, item_condition, price,
                      allowed_block_id, allowed_year, available_from, available_until) VALUES
  (2, 1, 'Engineering Mathematics by B.S. Grewal', '43rd edition, a few pencil marks.',            'good',     350,  NULL, NULL, NULL, NULL),   -- 1
  (2, 2, 'Hero Sprint cycle with lock',            'Gears work, new tyres last semester.',         'fair',     2500, NULL, NULL, NULL, NULL),   -- 2
  (3, 3, 'boAt Rockerz 450 headphones',            'Bluetooth, 1 year old, with box.',             'like_new', 900,  NULL, NULL, NULL, NULL),   -- 3
  (3, 4, 'Electric kettle 1.5L',                   'Only for my block, pick up from room B-214.', 'good',     400,  2,    NULL, NULL, NULL),   -- 4
  (2, 8, 'Engineering drawing kit',                'Mini drafter, compass and sheets. For first years.', 'good', 250, NULL, 1,  NULL, NULL),   -- 5
  (3, 5, 'Study table lamp',                       'LED, three brightness levels. Evenings only.', 'new',      300,  NULL, NULL, '18:00', '23:00'), -- 6
  (5, 1, 'Let Us C by Yashavant Kanetkar',         'Clean copy.',                                  'good',     200,  NULL, NULL, NULL, NULL),   -- 7
  (5, 7, 'Yonex badminton racket',                 'Comes with a cover and 3 shuttles.',           'good',     800,  NULL, NULL, NULL, NULL),   -- 8
  (2, 6, 'College hoodie (size M)',                'Worn twice.',                                  'like_new', 450,  NULL, NULL, NULL, NULL),   -- 9
  (3, 9, 'Mini fridge 45L',                        'Cools well, slight dent on the side.',         'fair',     3500, NULL, NULL, NULL, NULL);   -- 10

-- Orders are created through the stored functions so that the triggers and the
-- audit trail behave exactly as they do in the application.
SELECT fn_request_order(1, 3, 'Can I collect it tonight?');   -- order 1: Riya -> Aman's book
SELECT fn_request_order(3, 4, NULL);                           -- order 2: Kabir -> Riya's headphones
SELECT fn_request_order(3, 2, 'Is the box included?');         -- order 3: Aman  -> Riya's headphones
SELECT fn_request_order(2, 4, NULL);                           -- order 4: Kabir -> Aman's cycle
SELECT fn_request_order(9, 4, NULL);                           -- order 5: Kabir -> Aman's hoodie

-- Order 1 goes all the way to completed.
SELECT fn_transition_order(1, 2, 'accept');
SELECT fn_transition_order(1, 3, 'choose_payment', 'upi');
SELECT fn_transition_order(1, 2, 'confirm_payment');
SELECT fn_transition_order(1, 2, 'handover');
SELECT fn_transition_order(1, 3, 'complete');

-- Riya accepts Aman's request for the headphones; Kabir's request stays on the waitlist.
SELECT fn_transition_order(3, 3, 'accept');

-- Aman turns down Kabir's request for the hoodie.
SELECT fn_transition_order(5, 2, 'reject', NULL, 'Already promised to a friend');

-- Chats
SELECT fn_open_conversation(3, 4);   -- conversation 1: Kabir asks about the headphones
SELECT fn_open_conversation(1, 3);   -- conversation 2: Riya asks about the book

INSERT INTO messages (conversation_id, sender_id, body) VALUES
  (1, 4, 'Hi! Are the headphones still available?'),
  (1, 3, 'Reserved for someone right now, I will tell you if it falls through.'),
  (1, 4, 'Okay, thanks!'),
  (2, 3, 'Hi Aman, is the Grewal book the latest edition?'),
  (2, 2, 'Yes, 43rd edition. Come to A-105 after 8.');
