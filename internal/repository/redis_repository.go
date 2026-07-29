package repository

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisRepository interface {
	WarmupStock(ctx context.Context, eventID, zoneID string, capacity int) error
	ReserveTicketAtomic(ctx context.Context, eventID, zoneID string, quantity int) (int64, error)
	EnqueueUser(ctx context.Context, eventID, userID string) (int64, error)
	GetQueuePosition(ctx context.Context, eventID, userID string) (int64, error)
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

// EnqueueUser: Save user into ZSER queue using Current Timestamp (Nanoseconds) as Score
func (r *redisRepository) EnqueueUser(ctx context.Context, eventID, userID string) (int64, error) {
	key := fmt.Sprintf("event:%s:queue", eventID)
	timestamp := float64(time.Now().UnixNano())

	// ZAdd add User into Sorted Set
	err := r.rdb.ZAdd(ctx, key, redis.Z{
		Score:  timestamp,
		Member: userID,
	}).Err()

	if err != nil {
		return 0, err
	}

	// Return current queue position (Rank + 1)
	return r.GetQueuePosition(ctx, eventID, userID)
}

// GetQueuePosition: get current user's queue position from ZSER (0-indexed rank)
func (r *redisRepository) GetQueuePosition(ctx context.Context, eventID, userID string) (int64, error) {
	key := fmt.Sprintf("event:%s:queue", eventID)

	rank, err := r.rdb.ZRank(ctx, key, userID).Result()
	if err != nil {
		if err == redis.Nil {
			return -1, nil // No user found in queue
		}
		return 0, err
	}

	// ZRank return 0-based index, so that the proper queue position is rank + 1
	return rank + 1, nil
}
