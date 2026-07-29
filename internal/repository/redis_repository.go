package repository

import (
	"context"
	"fmt"
	"os"

	"github.com/redis/go-redis/v9"
)

type RedisRepository interface {
	WarmupStock(ctx context.Context, eventID, zoneID string, capacity int) error
	ReserveTicketAtomic(ctx context.Context, eventID, zoneID string, quantity int) (int64, error)
}

type redisRepository struct {
	rdb           *redis.Client
	reserveLuaSHA string // Cache Compiled SHA Script for highest Performance
}

func NewRedisRepository(rdb *redis.Client, luaScriptPath string) (RedisRepository, error) {
	// Read Lua Script file
	scriptContent, err := os.ReadFile(luaScriptPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read lua script: %w", err)
	}

	// Load Script into Redis Memory to get SHA Digest (faster script injecting)
	sha, err := rdb.ScriptLoad(context.Background(), string(scriptContent)).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to load lua script into redis: %w", err)
	}

	return &redisRepository{
		rdb:           rdb,
		reserveLuaSHA: sha,
	}, nil
}

// WarmupStock: Get stock from PostgreSQL, setup in Redis
func (r *redisRepository) WarmupStock(ctx context.Context, eventID, zoneID string, capacity int) error {
	key := fmt.Sprintf("event:%s:zone:%s:stock", eventID, zoneID)
	return r.rdb.Set(ctx, key, capacity, 0).Err()
}

// ReserveTicketAtomic: Run Lua Script via SHA to reserve ticket atomically (<1ms Execution Time)
func (r *redisRepository) ReserveTicketAtomic(ctx context.Context, eventID, zoneID string, quantity int) (int64, error) {
	key := fmt.Sprintf("event:%s:zone:%s:stock", eventID, zoneID)

	// EVALSHA Run script via SHA Hash on Redis Memory
	res, err := r.rdb.EvalSha(ctx, r.reserveLuaSHA, []string{key}, quantity).Result()
	if err != nil {
		return 0, err
	}

	return res.(int64), nil
}
