package repository

import (
	"context"
	"fmt"
	"os"
)

type RedisRepository interface {
	WarmupStock(ctx context.Context, eventID, zoneID string, capacity int) error
	ReserveTicketAtomic(ctx context.Context, eventID, zoneID string, quantity int) (int64, error)
}

type redisRepository struct {
	rdb           *redis.Client
	reserveLuaSHA string // Cache Compiled SHA Script เพื่อ Performance สูงสุด
}

func NewRedisRepository(rdb *redis.Client, luaScriptPath string) (RedisRepository, error) {
	// อ่านไฟล์ Lua Script
	scriptContent, err := os.ReadFile(luaScriptPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read lua script: %w", err)
	}

	// Load Script ลงใน Redis Memory เพื่อเอา SHA Digest (ช่วยให้ยิงสคริปต์ได้เร็วกว่าส่งไฟล์เต็ม)
	sha, err := rdb.ScriptLoad(context.Background(), string(scriptContent)).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to load lua script into redis: %w", err)
	}

	return &redisRepository{
		rdb:           rdb,
		reserveLuaSHA: sha,
	}, nil
}

// WarmupStock: นำสต็อกตั๋วจาก PostgreSQL มาตั้งค่าลง Redis ก่อนเปิดขายตั๋วจริง
func (r *redisRepository) WarmupStock(ctx context.Context, eventID, zoneID string, capacity int) error {
	key := fmt.Sprintf("event:%s:zone:%s:stock", eventID, zoneID)
	return r.rdb.Set(ctx, key, capacity, 0).Err()
}

// ReserveTicketAtomic: รัน Lua Script ผ่าน SHA เพื่อตัดสต็อกแบบ Atomic (<1ms Execution Time)
func (r *redisRepository) ReserveTicketAtomic(ctx context.Context, eventID, zoneID string, quantity int) (int64, error) {
	key := fmt.Sprintf("event:%s:zone:%s:stock", eventID, zoneID)

	// EVALSHA รันสคริปต์ผ่าน SHA Hash บน Redis Memory
	res, err := r.rdb.EvalSha(ctx, r.reserveLuaSHA, []string{key}, quantity).Result()
	if err != nil {
		return 0, err
	}

	return res.(int64), nil
}
