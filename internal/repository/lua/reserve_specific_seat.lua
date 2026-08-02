local hash_key = KEYS[1]
local hold_key = KEYS[2]
local seat_id = ARGV[1]
local user_id = ARGV[2]
local ttl_seconds = tonumber(ARGV[3])

local current = redis.call("HGET", hash_key, seat_id)

if current and current ~= "AVAILABLE" then
    return 0
end

redis.call("HSET", hash_key, seat_id, "HELD:" .. user_id)
redis.call("SETEX", hold_key, ttl_seconds, user_id)

return 1
