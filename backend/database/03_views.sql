-- =============================================================================
-- Hostel Market - 03_views.sql
-- Views give the API (and the report) ready-made joins and summaries.
-- =============================================================================

-- Users without the password hash, with their block and hostel resolved.
CREATE OR REPLACE VIEW v_user_profiles AS
SELECT u.user_id, u.email, u.full_name, u.role, u.is_verified, u.verified_at,
       u.year_of_study, u.upi_id,
       b.block_id, b.name AS block_name,
       h.hostel_id, h.name AS hostel_name,
       u.created_at, u.updated_at
FROM users u
JOIN blocks  b ON b.block_id  = u.block_id
JOIN hostels h ON h.hostel_id = b.hostel_id;

-- One row per listing with category, seller, hostel and cover photo: what a feed tile needs.
CREATE OR REPLACE VIEW v_listing_cards AS
SELECT l.listing_id, l.title, l.description, l.price, l.item_condition, l.status,
       c.category_id, c.slug AS category, c.name AS category_name,
       l.seller_id, u.full_name AS seller_name, sb.name AS seller_block,
       h.hostel_id, h.name AS hostel_name,
       l.available_from, l.available_until,
       l.allowed_block_id, ab.name AS allowed_block, l.allowed_year,
       (SELECT p.url FROM listing_photos p
         WHERE p.listing_id = l.listing_id ORDER BY p.position LIMIT 1) AS cover_url,
       (SELECT count(*) FROM listing_photos p WHERE p.listing_id = l.listing_id) AS photo_count,
       l.search_doc, l.created_at, l.updated_at, l.removed_at
FROM listings l
JOIN categories c  ON c.category_id = l.category_id
JOIN users      u  ON u.user_id     = l.seller_id
JOIN blocks     sb ON sb.block_id   = u.block_id
JOIN hostels    h  ON h.hostel_id   = sb.hostel_id
LEFT JOIN blocks ab ON ab.block_id  = l.allowed_block_id;

-- Orders with the listing, buyer and seller resolved.
CREATE OR REPLACE VIEW v_order_details AS
SELECT o.order_id, o.listing_id, l.title AS listing_title, l.status AS listing_status,
       (SELECT p.url FROM listing_photos p
         WHERE p.listing_id = l.listing_id ORDER BY p.position LIMIT 1) AS listing_cover_url,
       o.buyer_id, bu.full_name AS buyer_name,
       l.seller_id, su.full_name AS seller_name, su.upi_id AS seller_upi_id,
       o.price, o.status, o.payment_method, o.buyer_note,
       o.request_expires_at, o.accepted_at, o.paid_at, o.handed_over_at, o.completed_at,
       o.closed_at, o.close_reason, o.created_at, o.updated_at
FROM orders o
JOIN listings l  ON l.listing_id = o.listing_id
JOIN users    bu ON bu.user_id   = o.buyer_id
JOIN users    su ON su.user_id   = l.seller_id;

-- Per-seller statistics (aggregates with FILTER and LEFT JOIN, so sellers with no
-- sales still appear). Only one paid order can exist per listing, so the join
-- does not double-count listings.
CREATE OR REPLACE VIEW v_seller_stats AS
SELECT u.user_id, u.full_name,
       count(l.listing_id) FILTER (WHERE l.status = 'active')   AS active_listings,
       count(l.listing_id) FILTER (WHERE l.status = 'reserved') AS reserved_listings,
       count(l.listing_id) FILTER (WHERE l.status = 'sold')     AS sold_listings,
       COALESCE(sum(o.price), 0)                                AS total_earned
FROM users u
LEFT JOIN listings l ON l.seller_id = u.user_id
LEFT JOIN orders   o ON o.listing_id = l.listing_id
                    AND o.status IN ('paid', 'handed_over', 'completed')
GROUP BY u.user_id, u.full_name;

-- Market summary per hostel and category (active listings only).
CREATE OR REPLACE VIEW v_hostel_category_summary AS
SELECT hostel_id, hostel_name, category, category_name,
       count(*)            AS active_listings,
       round(avg(price))   AS avg_price,
       min(price)          AS min_price,
       max(price)          AS max_price
FROM v_listing_cards
WHERE status = 'active'
GROUP BY hostel_id, hostel_name, category, category_name;
