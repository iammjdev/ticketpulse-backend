local stock_key = KEYS[1]
local quantity = tonumber(ARGV[1])

local current_stock = redis.call("GET", stock_key)

if not current_stock then
    return -1
end

local stock_num = tonumber(current_stock)

if stock_num < quantity then
    return 0
end

redis.call("DECRBY", stock_key, quantity)

return 1