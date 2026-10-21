-- Validates and applies one bid atomically. Two concurrent bids are serialised by
-- Redis, so they can never both become the highest bid.
--
-- KEYS[1]  auction:{listing_id} hash
-- ARGV[1]  bidder id
-- ARGV[2]  amount (paise)
-- ARGV[3]  bid token (identifies this attempt, for a later revert)
-- ARGV[4]  auction round the caller validated against
-- ARGV[5]  anti-snipe window (ms): a bid this close to the end extends it...
-- ARGV[6]  ...by this much (ms)
--
-- Returns {status, ...}:
--   {"ok", prev_high, prev_bidder, prev_token, prev_ends, new_ends, extended}
--   {"too_low", min_required}
--   {"missing"} | {"round_mismatch"} | {"closed"} | {"ended"} | {"own_listing"} | {"already_highest"}
local key = KEYS[1]
if redis.call('EXISTS', key) == 0 then
  return {'missing'}
end

local s = redis.call('HMGET', key, 'round', 'seller', 'start', 'incr', 'ends', 'closed', 'high', 'bidder', 'token')
local round, seller = tonumber(s[1]), s[2]
local start, incr, ends = tonumber(s[3]), tonumber(s[4]), tonumber(s[5])
local closed, high, bidder, token = s[6], tonumber(s[7]) or 0, s[8] or '', s[9] or ''

if round ~= tonumber(ARGV[4]) then
  return {'round_mismatch'}
end
if closed == '1' then
  return {'closed'}
end

local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
if now >= ends then
  return {'ended'}
end

local who, amount = ARGV[1], tonumber(ARGV[2])
if who == seller then
  return {'own_listing'}
end
if who == bidder then
  return {'already_highest'}
end

local min = start
if high > 0 then
  min = high + incr
end
if amount < min then
  return {'too_low', tostring(min)}
end

local new_ends, extended = ends, 0
if ends - now <= tonumber(ARGV[5]) then
  new_ends = ends + tonumber(ARGV[6])
  extended = 1
end

redis.call('HSET', key, 'high', amount, 'bidder', who, 'token', ARGV[3], 'ends', new_ends)
redis.call('PEXPIREAT', key, new_ends + 86400000)
return {'ok', tostring(high), bidder, token, tostring(ends), tostring(new_ends), tostring(extended)}
