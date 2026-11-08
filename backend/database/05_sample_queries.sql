-- =============================================================================
-- Hostel Market - 05_sample_queries.sql
-- Example queries for the report (Chapter 5.3 / Appendix A). Run them after the
-- seed data in psql or any SQL client and screenshot the output.
-- =============================================================================

-- ---------- Simple retrieval ----------
-- Q1. All hostels and their blocks (JOIN + ORDER BY)
SELECT h.name AS hostel, b.name AS block
FROM hostels h JOIN blocks b ON b.hostel_id = h.hostel_id
ORDER BY h.name, b.name;

-- Q2. Search a user by email
SELECT user_id, full_name, email, is_verified FROM users WHERE email = 'aman@college.edu';

-- Q3. Active listings under Rs 500, cheapest first
SELECT listing_id, title, price FROM listings
WHERE status = 'active' AND price < 500
ORDER BY price;

-- ---------- Joins ----------
-- Q4. Every listing with its category, seller and the seller's hostel (4-table join)
SELECT l.listing_id, l.title, c.name AS category, u.full_name AS seller, h.name AS hostel
FROM listings l
JOIN categories c ON c.category_id = l.category_id
JOIN users u      ON u.user_id     = l.seller_id
JOIN blocks b     ON b.block_id    = u.block_id
JOIN hostels h    ON h.hostel_id   = b.hostel_id;

-- Q5. Orders with buyer and seller names (self-join on users through listings)
SELECT o.order_id, l.title, buyer.full_name AS buyer, seller.full_name AS seller, o.status, o.price
FROM orders o
JOIN listings l    ON l.listing_id = o.listing_id
JOIN users buyer   ON buyer.user_id = o.buyer_id
JOIN users seller  ON seller.user_id = l.seller_id
ORDER BY o.order_id;

-- Q6. Users who have never listed anything (LEFT JOIN ... IS NULL)
SELECT u.user_id, u.full_name
FROM users u LEFT JOIN listings l ON l.seller_id = u.user_id
WHERE l.listing_id IS NULL;

-- ---------- Aggregates and grouping ----------
-- Q7. Number of active listings and average price per category
SELECT c.name AS category, count(l.listing_id) AS listings, round(avg(l.price), 2) AS avg_price
FROM categories c LEFT JOIN listings l ON l.category_id = c.category_id AND l.status = 'active'
GROUP BY c.name
ORDER BY listings DESC;

-- Q8. Listings that received more than one buy request (GROUP BY ... HAVING)
SELECT l.listing_id, l.title, count(*) AS requests
FROM orders o JOIN listings l ON l.listing_id = o.listing_id
GROUP BY l.listing_id, l.title
HAVING count(*) > 1;

-- Q9. Orders per status
SELECT status, count(*) AS orders FROM orders GROUP BY status ORDER BY orders DESC;

-- ---------- Nested queries ----------
-- Q10. Listings priced above the average of their own category (correlated subquery)
SELECT l.title, l.price
FROM listings l
WHERE l.price > (SELECT avg(x.price) FROM listings x WHERE x.category_id = l.category_id);

-- Q11. Sellers who have sold at least one item (IN subquery)
SELECT full_name FROM users
WHERE user_id IN (SELECT l.seller_id FROM listings l WHERE l.status = 'sold');

-- Q12. Buyers with a pending request (EXISTS)
SELECT u.full_name FROM users u
WHERE EXISTS (SELECT 1 FROM orders o WHERE o.buyer_id = u.user_id AND o.status = 'requested');

-- ---------- Full-text search ----------
-- Q13. Search listings for "engineering" ranked by relevance (GIN index on search_doc)
SELECT title, ts_rank(search_doc, q) AS rank
FROM listings, websearch_to_tsquery('english', 'engineering') q
WHERE search_doc @@ q
ORDER BY rank DESC;

-- ---------- Views ----------
-- Q14. Feed tiles for Aryabhatta Hostel
SELECT listing_id, title, price, category_name, seller_name FROM v_listing_cards
WHERE hostel_name = 'Aryabhatta Hostel' AND status = 'active';

-- Q15. Seller leaderboard
SELECT * FROM v_seller_stats ORDER BY total_earned DESC, sold_listings DESC;

-- Q16. Market summary
SELECT * FROM v_hostel_category_summary ORDER BY hostel_name, active_listings DESC;

-- ---------- Functions, procedures and triggers ----------
-- Q17. Can Rohan (unverified, user 6) buy listing 2? Returns the reason code.
SELECT fn_buy_block_reason(2, 6) AS reason;

-- Q18. Audit trail written by the orders trigger
SELECT h.order_id, h.old_status, h.new_status, u.full_name AS changed_by, h.note, h.changed_at
FROM order_status_history h LEFT JOIN users u ON u.user_id = h.changed_by
WHERE h.order_id = 1 ORDER BY h.history_id;

-- Q19. A transaction: Kabir requests Aman's cycle (order 4 exists already, so this
-- fails with "already_requested" and nothing is changed: atomicity).
BEGIN;
SELECT fn_request_order(2, 4, 'second try');
ROLLBACK;

-- Q20. Constraint demos (each statement is expected to FAIL)
-- INSERT INTO users (email, password_hash, full_name, block_id, year_of_study)
--   VALUES ('aman@college.edu', 'x', 'Duplicate', 1, 2);                -- UNIQUE violation
-- INSERT INTO listings (seller_id, category_id, title, item_condition, price)
--   VALUES (2, 1, 'Bad price', 'good', -5);                              -- CHECK violation
-- INSERT INTO orders (listing_id, buyer_id, price, request_expires_at)
--   VALUES (999, 2, 100, now());                                         -- FOREIGN KEY violation
-- INSERT INTO orders (listing_id, buyer_id, price, request_expires_at)
--   VALUES (1, 2, 100, now());                                           -- trigger: own listing

-- ---------- Update and delete ----------
-- Q21. Seller lowers a price
UPDATE listings SET price = 2200 WHERE listing_id = 2 AND status = 'active';

-- Q22. Admin verifies Rohan (stored procedure)
CALL sp_set_user_verified(6, true);

-- Q23. Delete a chat message (use carefully)
DELETE FROM messages WHERE message_id = 3;
