-- GCRA (generic cell rate algorithm): a token bucket that stores a single number,
-- the "theoretical arrival time" (TAT) of the next request, in milliseconds.
--
-- KEYS[1]  bucket key
-- ARGV[1]  emission interval in ms (window / limit): how much TAT advances per request
-- ARGV[2]  capacity in ms (interval * limit): the most TAT may run ahead of now
--
-- Returns {allowed (1|0), remaining, retry_after_ms, reset_after_ms}.
-- Redis TIME is the single clock, so app-server clock skew can't matter.
local interval = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])

local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)

local tat = tonumber(redis.call('GET', KEYS[1]))
if not tat or tat < now then
  tat = now
end

local new_tat = tat + interval
local backlog = new_tat - now
if backlog > capacity then
  -- Rejected requests consume nothing; one slot frees up after backlog - capacity.
  return {0, 0, backlog - capacity, tat - now}
end

redis.call('SET', KEYS[1], new_tat, 'PX', backlog)
return {1, math.floor((capacity - backlog) / interval), 0, backlog}
