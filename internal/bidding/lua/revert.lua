-- Undoes a bid whose Postgres write failed, but only if it is still the highest bid:
-- if someone has already outbid it, their state is newer and must stay.
--
-- KEYS[1]  auction:{listing_id}
-- ARGV[1]  token of the failed bid
-- ARGV[2..5] previous high, bidder, token, ends
local key = KEYS[1]
if redis.call('HGET', key, 'token') ~= ARGV[1] then
  return 0
end
redis.call('HSET', key, 'high', ARGV[2], 'bidder', ARGV[3], 'token', ARGV[4], 'ends', ARGV[5])
return 1
