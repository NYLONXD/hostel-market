-- Stops accepting bids for a round once its end time has passed. Idempotent.
--
-- KEYS[1]  auction:{listing_id}
-- ARGV[1]  round
-- Returns {"closed"} | {"not_due", ends_ms} | {"missing"} | {"round_mismatch"}
local key = KEYS[1]
if redis.call('EXISTS', key) == 0 then
  return {'missing'}
end
local s = redis.call('HMGET', key, 'round', 'ends')
if tonumber(s[1]) ~= tonumber(ARGV[1]) then
  return {'round_mismatch'}
end
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
if now < tonumber(s[2]) then
  return {'not_due', s[2]}
end
redis.call('HSET', key, 'closed', '1')
return {'closed'}
