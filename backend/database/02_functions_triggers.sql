-- =============================================================================
-- Hostel Market - 02_functions_triggers.sql
-- Business rules that live inside the database: helper functions, triggers,
-- stored functions for multi-step transactions, and stored procedures.
--
-- Errors raised for the application use custom SQLSTATEs 'HM403', 'HM404',
-- 'HM409' and 'HM422' (HTTP status = the last three digits). DETAIL carries a
-- short machine-readable code such as 'own_listing'. The API turns these into
-- JSON error responses.
-- =============================================================================

-- ---------------------------------------------------------------------------
-- Helpers
-- ---------------------------------------------------------------------------

-- Raise an application error. Example: PERFORM fn_raise(404, 'not_found', 'order not found');
CREATE OR REPLACE FUNCTION fn_raise(p_status integer, p_code text, p_message text)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION USING ERRCODE = 'HM' || p_status::text, MESSAGE = p_message, DETAIL = p_code;
END;
$$;

-- The hostel a user lives in (user -> block -> hostel).
CREATE OR REPLACE FUNCTION fn_user_hostel(p_user_id bigint)
RETURNS integer LANGUAGE sql STABLE AS $$
  SELECT b.hostel_id FROM users u JOIN blocks b ON b.block_id = u.block_id WHERE u.user_id = p_user_id;
$$;

-- Is time t inside [from, until)? A window with from > until wraps past midnight,
-- so 22:00-06:00 covers late night and early morning.
CREATE OR REPLACE FUNCTION fn_time_in_window(t time, p_from time, p_until time)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE WHEN p_from < p_until THEN t >= p_from AND t < p_until
              ELSE t >= p_from OR t < p_until END;
$$;

-- Human-readable text for each "cannot buy" reason code.
CREATE OR REPLACE FUNCTION fn_reason_message(p_code text)
RETURNS text LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE p_code
    WHEN 'listing_not_found'   THEN 'listing not found'
    WHEN 'listing_unavailable' THEN 'this listing is no longer available'
    WHEN 'own_listing'         THEN 'you cannot buy your own listing'
    WHEN 'not_verified'        THEN 'verify your account before buying'
    WHEN 'different_hostel'    THEN 'this listing is only open to residents of the seller''s hostel'
    WHEN 'block_restricted'    THEN 'this listing is restricted to residents of one block'
    WHEN 'year_restricted'     THEN 'this listing is restricted to students of one year'
    WHEN 'outside_time_window' THEN 'this listing can only be bought during the seller''s available hours'
    ELSE p_code
  END;
$$;

-- ---------------------------------------------------------------------------
-- Generic trigger: keep updated_at current
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION trg_set_updated_at()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END;
$$;

CREATE TRIGGER users_updated_at    BEFORE UPDATE ON users    FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();
CREATE TRIGGER listings_updated_at BEFORE UPDATE ON listings FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();
CREATE TRIGGER orders_updated_at   BEFORE UPDATE ON orders   FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();

-- ---------------------------------------------------------------------------
-- LISTINGS triggers
-- ---------------------------------------------------------------------------

-- Rules a CHECK constraint cannot express because they need other tables:
--  * only verified users with a UPI ID may create listings;
--  * the "allowed block" rule must name a block of the seller's own hostel;
--  * removed_at follows status.
CREATE OR REPLACE FUNCTION trg_listings_validate()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  v_seller users%ROWTYPE;
BEGIN
  IF TG_OP = 'INSERT' THEN
    SELECT * INTO v_seller FROM users WHERE user_id = NEW.seller_id;
    IF FOUND AND NOT v_seller.is_verified THEN
      PERFORM fn_raise(403, 'not_verified', 'verify your account before selling');
    END IF;
    IF FOUND AND v_seller.upi_id IS NULL THEN
      PERFORM fn_raise(403, 'upi_required', 'add a UPI ID to your profile before selling');
    END IF;
  END IF;

  IF NEW.allowed_block_id IS NOT NULL
     AND (TG_OP = 'INSERT' OR NEW.allowed_block_id IS DISTINCT FROM OLD.allowed_block_id)
     AND (SELECT hostel_id FROM blocks WHERE block_id = NEW.allowed_block_id) <> fn_user_hostel(NEW.seller_id) THEN
    PERFORM fn_raise(422, 'invalid_block', 'the allowed block must be in your own hostel');
  END IF;

  IF NEW.status = 'removed' THEN
    NEW.removed_at := COALESCE(NEW.removed_at, now());
  ELSE
    NEW.removed_at := NULL;
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER listings_validate BEFORE INSERT OR UPDATE ON listings
  FOR EACH ROW EXECUTE FUNCTION trg_listings_validate();

-- When an item is sold or taken down, everyone still waiting is turned away.
CREATE OR REPLACE FUNCTION trg_listings_close_requests()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE orders
     SET status = 'rejected', closed_at = now(),
         close_reason = CASE NEW.status WHEN 'sold' THEN 'item sold to another buyer' ELSE 'listing removed' END
   WHERE listing_id = NEW.listing_id AND status = 'requested';
  RETURN NULL;
END;
$$;

CREATE TRIGGER listings_close_requests AFTER UPDATE OF status ON listings
  FOR EACH ROW WHEN (NEW.status IN ('sold', 'removed') AND OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION trg_listings_close_requests();

-- ---------------------------------------------------------------------------
-- ORDERS triggers
-- ---------------------------------------------------------------------------

-- A buyer can never order their own item, even with a hand-written INSERT.
CREATE OR REPLACE FUNCTION trg_orders_not_own_listing()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF EXISTS (SELECT 1 FROM listings WHERE listing_id = NEW.listing_id AND seller_id = NEW.buyer_id) THEN
    PERFORM fn_raise(403, 'own_listing', 'you cannot buy your own listing');
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER orders_not_own_listing BEFORE INSERT ON orders
  FOR EACH ROW EXECUTE FUNCTION trg_orders_not_own_listing();

-- Audit trail: every status change is written to order_status_history.
-- The acting user is read from the transaction-local setting app.actor_id
-- (set by the stored functions below); empty means "system".
CREATE OR REPLACE FUNCTION trg_orders_history()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' OR OLD.status IS DISTINCT FROM NEW.status THEN
    INSERT INTO order_status_history (order_id, old_status, new_status, changed_by, note)
    VALUES (NEW.order_id,
            CASE WHEN TG_OP = 'UPDATE' THEN OLD.status END,
            NEW.status,
            NULLIF(current_setting('app.actor_id', true), '')::bigint,
            CASE WHEN NEW.status IN ('rejected', 'cancelled', 'expired') THEN NEW.close_reason END);
  END IF;
  RETURN NULL;
END;
$$;

CREATE TRIGGER orders_history AFTER INSERT OR UPDATE OF status ON orders
  FOR EACH ROW EXECUTE FUNCTION trg_orders_history();

-- ---------------------------------------------------------------------------
-- MESSAGES triggers
-- ---------------------------------------------------------------------------

-- Only the buyer and the seller of a conversation may post in it.
CREATE OR REPLACE FUNCTION trg_messages_check_sender()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (
       SELECT 1 FROM conversations c JOIN listings l ON l.listing_id = c.listing_id
        WHERE c.conversation_id = NEW.conversation_id AND NEW.sender_id IN (c.buyer_id, l.seller_id)) THEN
    PERFORM fn_raise(403, 'not_participant', 'you are not part of this conversation');
  END IF;
  NEW.body := trim(NEW.body);
  RETURN NEW;
END;
$$;

CREATE TRIGGER messages_check_sender BEFORE INSERT ON messages
  FOR EACH ROW EXECUTE FUNCTION trg_messages_check_sender();

-- After a message: bump the conversation's recency and mark it read for its sender.
CREATE OR REPLACE FUNCTION trg_messages_touch_conversation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE conversations c
     SET last_message_at     = NEW.created_at,
         buyer_last_read_id  = CASE WHEN c.buyer_id =  NEW.sender_id THEN NEW.message_id ELSE c.buyer_last_read_id END,
         seller_last_read_id = CASE WHEN c.buyer_id <> NEW.sender_id THEN NEW.message_id ELSE c.seller_last_read_id END
   WHERE c.conversation_id = NEW.conversation_id;
  RETURN NULL;
END;
$$;

CREATE TRIGGER messages_touch_conversation AFTER INSERT ON messages
  FOR EACH ROW EXECUTE FUNCTION trg_messages_touch_conversation();

-- ---------------------------------------------------------------------------
-- Stored functions (each runs as ONE transaction when called)
-- ---------------------------------------------------------------------------

-- Why can't this buyer buy this listing right now? NULL means they can.
-- Usable directly in queries:  SELECT fn_buy_block_reason(5, 3);
CREATE OR REPLACE FUNCTION fn_buy_block_reason(p_listing_id bigint, p_buyer_id bigint, p_at timestamptz DEFAULT now())
RETURNS text LANGUAGE plpgsql STABLE AS $$
DECLARE
  l listings%ROWTYPE;
  b users%ROWTYPE;
BEGIN
  SELECT * INTO l FROM listings WHERE listing_id = p_listing_id;
  IF NOT FOUND THEN RETURN 'listing_not_found'; END IF;
  SELECT * INTO b FROM users WHERE user_id = p_buyer_id;

  IF l.status <> 'active'            THEN RETURN 'listing_unavailable'; END IF;
  IF l.seller_id = p_buyer_id        THEN RETURN 'own_listing'; END IF;
  IF NOT b.is_verified               THEN RETURN 'not_verified'; END IF;
  IF fn_user_hostel(l.seller_id) <> fn_user_hostel(p_buyer_id) THEN RETURN 'different_hostel'; END IF;
  IF l.allowed_block_id IS NOT NULL AND l.allowed_block_id <> b.block_id THEN RETURN 'block_restricted'; END IF;
  IF l.allowed_year IS NOT NULL AND l.allowed_year <> b.year_of_study  THEN RETURN 'year_restricted'; END IF;
  IF l.available_from IS NOT NULL
     AND NOT fn_time_in_window((p_at AT TIME ZONE 'Asia/Kolkata')::time, l.available_from, l.available_until) THEN
    RETURN 'outside_time_window';
  END IF;
  RETURN NULL;
END;
$$;

-- Create a buy request. The listing row is locked (SELECT ... FOR UPDATE) so the
-- eligibility check and the insert see the same listing state.
CREATE OR REPLACE FUNCTION fn_request_order(p_listing_id bigint, p_buyer_id bigint, p_note text DEFAULT NULL,
                                            p_ttl interval DEFAULT interval '48 hours')
RETURNS orders LANGUAGE plpgsql AS $$
DECLARE
  l      listings%ROWTYPE;
  o      orders%ROWTYPE;
  reason text;
BEGIN
  SELECT * INTO l FROM listings WHERE listing_id = p_listing_id FOR UPDATE;
  IF NOT FOUND THEN
    PERFORM fn_raise(404, 'not_found', 'listing not found');
  END IF;

  reason := fn_buy_block_reason(p_listing_id, p_buyer_id);
  IF reason IS NOT NULL THEN
    PERFORM fn_raise(CASE WHEN reason = 'listing_unavailable' THEN 409 ELSE 403 END, reason, fn_reason_message(reason));
  END IF;

  IF EXISTS (SELECT 1 FROM orders WHERE listing_id = p_listing_id AND buyer_id = p_buyer_id AND status = 'requested') THEN
    PERFORM fn_raise(409, 'already_requested', 'you already have a pending request for this item');
  END IF;

  PERFORM set_config('app.actor_id', p_buyer_id::text, true);
  INSERT INTO orders (listing_id, buyer_id, price, buyer_note, request_expires_at)
  VALUES (l.listing_id, p_buyer_id, l.price, NULLIF(trim(p_note), ''), now() + p_ttl)
  RETURNING * INTO o;
  RETURN o;
END;
$$;

-- The order state machine. p_action is one of:
--   accept | reject | cancel | choose_payment | confirm_payment | handover | complete
-- Locks are always taken listing first, then order, so two concurrent actions on the
-- same item queue up instead of deadlocking.
CREATE OR REPLACE FUNCTION fn_transition_order(p_order_id bigint, p_actor_id bigint, p_action text,
                                               p_payment_method text DEFAULT NULL, p_reason text DEFAULT NULL)
RETURNS orders LANGUAGE plpgsql AS $$
DECLARE
  o          orders%ROWTYPE;
  l          listings%ROWTYPE;
  v_actor    text;     -- who may do it: buyer | seller | either
  v_from     text[];   -- statuses it may start from
  v_to       text;     -- status it moves to
  v_was_live boolean;
BEGIN
  SELECT * INTO o FROM orders WHERE order_id = p_order_id;
  IF NOT FOUND THEN
    PERFORM fn_raise(404, 'not_found', 'order not found');
  END IF;
  SELECT * INTO l FROM listings WHERE listing_id = o.listing_id FOR UPDATE;
  SELECT * INTO o FROM orders WHERE order_id = p_order_id FOR UPDATE;

  -- Strangers get "not found" so other people's orders are not revealed.
  IF p_actor_id IS DISTINCT FROM o.buyer_id AND p_actor_id IS DISTINCT FROM l.seller_id THEN
    PERFORM fn_raise(404, 'not_found', 'order not found');
  END IF;

  CASE p_action
    WHEN 'accept'          THEN v_actor := 'seller'; v_from := ARRAY['requested'];  v_to := 'accepted';
    WHEN 'reject'          THEN v_actor := 'seller'; v_from := ARRAY['requested'];  v_to := 'rejected';
    WHEN 'cancel'          THEN v_actor := 'either'; v_from := ARRAY['requested', 'accepted', 'awaiting_payment']; v_to := 'cancelled';
    WHEN 'choose_payment'  THEN v_actor := 'buyer';  v_from := ARRAY['accepted'];   v_to := 'awaiting_payment';
    WHEN 'confirm_payment' THEN v_actor := 'seller'; v_from := ARRAY['awaiting_payment']; v_to := 'paid';
    WHEN 'handover'        THEN v_actor := 'seller'; v_from := ARRAY['paid'];       v_to := 'handed_over';
    WHEN 'complete'        THEN v_actor := 'buyer';  v_from := ARRAY['handed_over']; v_to := 'completed';
    ELSE
      PERFORM fn_raise(422, 'invalid_action', format('unknown action "%s"', p_action));
  END CASE;

  IF v_actor = 'buyer' AND p_actor_id <> o.buyer_id THEN
    PERFORM fn_raise(403, 'wrong_party', 'only the buyer can do this');
  ELSIF v_actor = 'seller' AND p_actor_id <> l.seller_id THEN
    PERFORM fn_raise(403, 'wrong_party', 'only the seller can do this');
  END IF;

  IF NOT (o.status = ANY (v_from)) THEN
    PERFORM fn_raise(409, 'invalid_transition',
                     format('cannot %s an order that is %s', replace(p_action, '_', ' '), replace(o.status, '_', ' ')));
  END IF;

  IF p_action = 'accept' THEN
    IF o.request_expires_at <= now() THEN
      PERFORM fn_raise(409, 'request_expired', 'this request has expired');
    ELSIF l.status = 'reserved' THEN
      PERFORM fn_raise(409, 'listing_reserved', 'this item is already reserved for another buyer');
    ELSIF l.status <> 'active' THEN
      PERFORM fn_raise(409, 'listing_unavailable', 'this item is no longer available');
    END IF;
  ELSIF p_action = 'cancel' AND o.status = 'requested' AND p_actor_id <> o.buyer_id THEN
    PERFORM fn_raise(409, 'use_reject', 'sellers reject requests instead of cancelling them');
  ELSIF p_action = 'choose_payment' AND (p_payment_method IS NULL OR p_payment_method NOT IN ('upi', 'cash')) THEN
    PERFORM fn_raise(422, 'invalid_payment_method', 'payment_method must be upi or cash');
  END IF;

  v_was_live := o.status IN ('accepted', 'awaiting_payment');
  PERFORM set_config('app.actor_id', p_actor_id::text, true);

  UPDATE orders SET
    status         = v_to,
    payment_method = CASE WHEN p_action = 'choose_payment' THEN p_payment_method ELSE payment_method END,
    accepted_at    = CASE WHEN v_to = 'accepted'    THEN now() ELSE accepted_at END,
    paid_at        = CASE WHEN v_to = 'paid'        THEN now() ELSE paid_at END,
    handed_over_at = CASE WHEN v_to = 'handed_over' THEN now() ELSE handed_over_at END,
    completed_at   = CASE WHEN v_to = 'completed'   THEN now() ELSE completed_at END,
    closed_at      = CASE WHEN v_to IN ('rejected', 'cancelled') THEN now() ELSE closed_at END,
    close_reason   = CASE WHEN v_to IN ('rejected', 'cancelled') THEN NULLIF(trim(p_reason), '') ELSE close_reason END
  WHERE order_id = o.order_id
  RETURNING * INTO o;

  -- Keep the listing's status in step with its live order.
  IF v_to = 'accepted' THEN
    UPDATE listings SET status = 'reserved' WHERE listing_id = l.listing_id;
  ELSIF v_to = 'cancelled' AND v_was_live AND l.status = 'reserved' THEN
    UPDATE listings SET status = 'active' WHERE listing_id = l.listing_id;
  ELSIF v_to = 'paid' THEN
    UPDATE listings SET status = 'sold' WHERE listing_id = l.listing_id;   -- trigger rejects the waitlist
  END IF;

  RETURN o;
END;
$$;

-- Expire buy requests the seller never answered. Called every minute by the API.
CREATE OR REPLACE FUNCTION fn_expire_requests()
RETURNS integer LANGUAGE plpgsql AS $$
DECLARE
  n integer;
BEGIN
  PERFORM set_config('app.actor_id', '', true);
  UPDATE orders
     SET status = 'expired', closed_at = now(), close_reason = 'seller did not respond in time'
   WHERE status = 'requested' AND request_expires_at <= now();
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n;
END;
$$;

-- Open (or return the existing) chat between a buyer and a listing's seller.
CREATE OR REPLACE FUNCTION fn_open_conversation(p_listing_id bigint, p_buyer_id bigint)
RETURNS conversations LANGUAGE plpgsql AS $$
DECLARE
  l listings%ROWTYPE;
  c conversations%ROWTYPE;
BEGIN
  SELECT * INTO l FROM listings WHERE listing_id = p_listing_id;
  IF NOT FOUND OR l.status = 'removed' THEN
    PERFORM fn_raise(404, 'not_found', 'listing not found');
  END IF;
  IF l.seller_id = p_buyer_id THEN
    PERFORM fn_raise(409, 'own_listing', 'you cannot start a chat about your own listing');
  END IF;
  IF NOT (SELECT is_verified FROM users WHERE user_id = p_buyer_id) THEN
    PERFORM fn_raise(403, 'not_verified', 'verify your account before messaging sellers');
  END IF;
  IF fn_user_hostel(l.seller_id) <> fn_user_hostel(p_buyer_id) THEN
    PERFORM fn_raise(403, 'different_hostel', fn_reason_message('different_hostel'));
  END IF;

  INSERT INTO conversations (listing_id, buyer_id) VALUES (p_listing_id, p_buyer_id)
  ON CONFLICT (listing_id, buyer_id) DO NOTHING;
  SELECT * INTO c FROM conversations WHERE listing_id = p_listing_id AND buyer_id = p_buyer_id;
  RETURN c;
END;
$$;

-- ---------------------------------------------------------------------------
-- Stored procedures (admin operations; run with CALL)
-- ---------------------------------------------------------------------------

-- CALL sp_set_user_verified(7, true);
CREATE OR REPLACE PROCEDURE sp_set_user_verified(p_user_id bigint, p_verified boolean)
LANGUAGE plpgsql AS $$
BEGIN
  UPDATE users
     SET is_verified = p_verified,
         verified_at = CASE WHEN p_verified THEN COALESCE(verified_at, now()) END
   WHERE user_id = p_user_id;
  IF NOT FOUND THEN
    PERFORM fn_raise(404, 'not_found', 'user not found');
  END IF;
END;
$$;

-- Take a listing down: cancel its live (unpaid) order, then mark it removed; the
-- listings trigger rejects any requests still waiting.
-- CALL sp_admin_remove_listing(12, 1, 'prohibited item');
CREATE OR REPLACE PROCEDURE sp_admin_remove_listing(p_listing_id bigint, p_admin_id bigint, p_reason text DEFAULT NULL)
LANGUAGE plpgsql AS $$
DECLARE
  v_status text;
BEGIN
  SELECT status INTO v_status FROM listings WHERE listing_id = p_listing_id FOR UPDATE;
  IF NOT FOUND THEN
    PERFORM fn_raise(404, 'not_found', 'listing not found');
  END IF;
  IF v_status = 'removed' THEN
    RETURN;
  END IF;

  PERFORM set_config('app.actor_id', p_admin_id::text, true);
  UPDATE orders
     SET status = 'cancelled', closed_at = now(),
         close_reason = COALESCE(NULLIF(trim(p_reason), ''), 'listing removed by admin')
   WHERE listing_id = p_listing_id AND status IN ('accepted', 'awaiting_payment');
  UPDATE listings SET status = 'removed' WHERE listing_id = p_listing_id;
END;
$$;
