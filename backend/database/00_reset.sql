-- =============================================================================
-- Hostel Market - 00_reset.sql
-- DESTRUCTIVE: drops every Hostel Market object (and all its data). Only touches
-- objects this project created, so it is safe on a shared hosted database.
-- =============================================================================

DROP VIEW IF EXISTS v_hostel_category_summary, v_seller_stats, v_order_details, v_listing_cards, v_user_profiles CASCADE;

DROP TABLE IF EXISTS messages, conversations, order_status_history, orders, listing_photos,
                     listings, categories, users, blocks, hostels CASCADE;

DROP PROCEDURE IF EXISTS sp_admin_remove_listing(bigint, bigint, text);
DROP PROCEDURE IF EXISTS sp_set_user_verified(bigint, boolean);

DROP FUNCTION IF EXISTS fn_open_conversation(bigint, bigint);
DROP FUNCTION IF EXISTS fn_expire_requests();
DROP FUNCTION IF EXISTS fn_transition_order(bigint, bigint, text, text, text);
DROP FUNCTION IF EXISTS fn_request_order(bigint, bigint, text, interval);
DROP FUNCTION IF EXISTS fn_buy_block_reason(bigint, bigint, timestamptz);
DROP FUNCTION IF EXISTS fn_reason_message(text);
DROP FUNCTION IF EXISTS fn_time_in_window(time, time, time);
DROP FUNCTION IF EXISTS fn_user_hostel(bigint);
DROP FUNCTION IF EXISTS fn_raise(integer, text, text);
DROP FUNCTION IF EXISTS trg_set_updated_at(), trg_listings_validate(), trg_listings_close_requests(),
                        trg_orders_not_own_listing(), trg_orders_history(),
                        trg_messages_check_sender(), trg_messages_touch_conversation();
