-- KEYS[1]: Redis Key สำหรับสต็อกของ zone นั้นๆ (เช่น event:uuid-123:zone:vip-a:stock)
-- ARGV[1]: จำนวนตั๋วที่ต้องการซื้อ (เช่น 1 หรือ 2)

local stock_key = KEYS[1]
local quantity = tonumber(ARGV[1])

-- 1. ตรวจสอบว่า Key สต็อกถูก Warm-up ใส่ Redis แล้วหรือยัง
local current_stock = redis.call("GET", stock_key)

if not current_stock then
    return -1 -- Return Code -1: Key ยังไม่ได้ถูก Warm-up ลง Redis Memory
end

local stock_num = tonumber(current_stock)

-- 2. ตรวจสอบว่าสต็อกเพียงพอกับจำนวนที่ต้องการจองหรือไม่
if stock_num < quantity then
    return 0 -- Return Code 0: สต็อกไม่พอ / บัตรหมด (Sold Out)
end

-- 3. ตัดสต็อกแบบ Atomic คำสั่งเดียว!
redis.call("DECRBY", stock_key, quantity)

return 1 -- Return Code 1: ตัดสต็อกสำเร็จ (Reserve Success)