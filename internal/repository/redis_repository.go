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
	ReserveTicket(ctx context.Context, eventID, zoneID string, quantity int) (string, error)
	EnqueueUser(ctx context.Context, eventID, userID string) (int64, error)
	GetQueuePosition(ctx context.Context, eventID, userID string) (int64, error)
	DequeueUser(ctx context.Context, eventID string, userID string) error

	// SetOrderExpiry arms the order:expire:{orderID} TTL key that the ExpirationWorker
	// listens for via Redis keyspace notifications — its expiry is what actually triggers
	// the unpaid-order cancellation, not a periodic DB scan.
	SetOrderExpiry(ctx context.Context, orderID string, ttl time.Duration) error
	// ClearOrderExpiry disarms the timer once an order is paid, so it never fires.
	ClearOrderExpiry(ctx context.Context, orderID string) error
	// OrderExpiryTTL reports seconds remaining on the timer (0 if missing/already fired) so
	// the frontend countdown can stay synced to the value that actually governs cancellation.
	OrderExpiryTTL(ctx context.Context, orderID string) (time.Duration, error)
	// RestoreZoneStock increments the same stock counter ReserveTicket decrements, undoing a
	// reservation whose order expired unpaid or was cancelled.
	RestoreZoneStock(ctx context.Context, eventID, zoneID string, quantity int) error
	// TryAcquireRateLimit atomically claims key for ttl (SET NX) and reports whether the
	// caller won the claim. Used to throttle the resend-email endpoint to 1 request/60s/order
	// without a DB round trip.
	TryAcquireRateLimit(ctx context.Context, key string, ttl time.Duration) (bool, error)
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
func (r *redisRepository) ReserveTicket(ctx context.Context, eventID, zoneID string, quantity int) (string, error) {
	key := fmt.Sprintf("event:%s:zone:%s:stock", eventID, zoneID)

	res, err := r.rdb.EvalSha(ctx, r.reserveLuaSHA, []string{key}, quantity).Result()
	if err != nil {
		return "ERROR", err
	}

	code := res.(int64)
	switch code {
	case 1:
		return "RESERVED", nil
	case 0:
		return "SOLD_OUT", nil
	default:
		return "NOT_WARMED_UP", fmt.Errorf("stock key not initialized in redis")
	}
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

// DequeueUser removes a user from the Redis Sorted Set queue after successful reservation
func (r *redisRepository) DequeueUser(ctx context.Context, eventID string, userID string) error {
	queueKey := fmt.Sprintf("event:%s:queue", eventID)
	return r.rdb.ZRem(ctx, queueKey, userID).Err()
}

func orderExpireKey(orderID string) string {
	return fmt.Sprintf("order:expire:%s", orderID)
}

func (r *redisRepository) SetOrderExpiry(ctx context.Context, orderID string, ttl time.Duration) error {
	return r.rdb.Set(ctx, orderExpireKey(orderID), "1", ttl).Err()
}

func (r *redisRepository) ClearOrderExpiry(ctx context.Context, orderID string) error {
	return r.rdb.Del(ctx, orderExpireKey(orderID)).Err()
}

func (r *redisRepository) OrderExpiryTTL(ctx context.Context, orderID string) (time.Duration, error) {
	ttl, err := r.rdb.TTL(ctx, orderExpireKey(orderID)).Result()
	if err != nil {
		return 0, err
	}
	// -1 = key exists without expiry (shouldn't happen here), -2 = key missing entirely.
	if ttl < 0 {
		return 0, nil
	}
	return ttl, nil
}

func (r *redisRepository) RestoreZoneStock(ctx context.Context, eventID, zoneID string, quantity int) error {
	key := fmt.Sprintf("event:%s:zone:%s:stock", eventID, zoneID)
	return r.rdb.IncrBy(ctx, key, int64(quantity)).Err()
}

func (r *redisRepository) TryAcquireRateLimit(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return r.rdb.SetNX(ctx, key, "1", ttl).Result()
}
