package repository

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	// TotalQueueLength sums the cardinality of every event's virtual-queue ZSET
	// (event:*:queue) for the admin stats dashboard.
	TotalQueueLength(ctx context.Context) (int64, error)
	// GetActiveHoldCount counts live order:expire:* keys — the 10-minute payment-hold timers
	// armed by SetOrderExpiry — for the admin stats dashboard.
	GetActiveHoldCount(ctx context.Context) (int64, error)
	// SetEventStatus mirrors an event's lifecycle status into event:{id}:status so hot-path
	// reservation/queue code can check it without a Postgres round trip.
	SetEventStatus(ctx context.Context, eventID, status string) error

	// ReserveSpecificSeat atomically claims one seat (as opposed to a zone-level quantity)
	// via the reserve_specific_seat Lua script, holding it in event:{eventID}:seat_status
	// for ttlSeconds. Returns false if the seat is already HELD or SOLD.
	ReserveSpecificSeat(ctx context.Context, eventID, seatID, userID string, ttlSeconds int) (bool, error)
	// GetSeatStatuses returns every non-AVAILABLE seat status for an event, self-healing any
	// HELD entry whose hold key already expired (hash fields carry no TTL of their own) back
	// to AVAILABLE. Seats absent from the returned map are AVAILABLE.
	GetSeatStatuses(ctx context.Context, eventID string) (map[string]string, error)

	// GetQueueReleaseRate reads the admin-configured virtual-queue dispatch rate (users/sec),
	// defaulting to defaultRate if never set.
	GetQueueReleaseRate(ctx context.Context, defaultRate int) (int, error)
	// SetQueueReleaseRate persists the admin-configured virtual-queue dispatch rate.
	SetQueueReleaseRate(ctx context.Context, rate int) error
	// IsQueuePaused reports whether an admin has emergency-paused virtual-queue dispatch.
	IsQueuePaused(ctx context.Context) (bool, error)
	// SetQueuePaused persists the emergency-pause flag for virtual-queue dispatch.
	SetQueuePaused(ctx context.Context, paused bool) error
	// FlushAllQueues deletes every event:*:queue ZSET, returning how many queued users were
	// dropped across all queues. An emergency admin action.
	FlushAllQueues(ctx context.Context) (int64, error)
}

type redisRepository struct {
	rdb               *redis.Client
	reserveLuaSHA     string // Cache Compiled SHA Script for highest Performance
	reserveSeatLuaSHA string
}

func loadLuaScript(ctx context.Context, rdb *redis.Client, scriptPath string) (string, error) {
	scriptContent, err := os.ReadFile(scriptPath)
	if err != nil {
		return "", fmt.Errorf("failed to read lua script %s: %w", scriptPath, err)
	}
	sha, err := rdb.ScriptLoad(ctx, string(scriptContent)).Result()
	if err != nil {
		return "", fmt.Errorf("failed to load lua script %s into redis: %w", scriptPath, err)
	}
	return sha, nil
}

func NewRedisRepository(rdb *redis.Client, luaScriptPath, reserveSeatLuaScriptPath string) (RedisRepository, error) {
	sha, err := loadLuaScript(context.Background(), rdb, luaScriptPath)
	if err != nil {
		return nil, err
	}

	seatSHA, err := loadLuaScript(context.Background(), rdb, reserveSeatLuaScriptPath)
	if err != nil {
		return nil, err
	}

	return &redisRepository{
		rdb:               rdb,
		reserveLuaSHA:     sha,
		reserveSeatLuaSHA: seatSHA,
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

// TotalQueueLength scans for every event:*:queue ZSET and sums their cardinalities. Used by
// the admin stats endpoint, which reports a platform-wide total rather than a per-event count.
func (r *redisRepository) TotalQueueLength(ctx context.Context) (int64, error) {
	var total int64
	var cursor uint64
	for {
		keys, next, err := r.rdb.Scan(ctx, cursor, "event:*:queue", 100).Result()
		if err != nil {
			return 0, err
		}
		for _, key := range keys {
			count, err := r.rdb.ZCard(ctx, key).Result()
			if err != nil {
				return 0, err
			}
			total += count
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return total, nil
}

func (r *redisRepository) SetEventStatus(ctx context.Context, eventID, status string) error {
	key := fmt.Sprintf("event:%s:status", eventID)
	return r.rdb.Set(ctx, key, status, 0).Err()
}

// GetActiveHoldCount scans for every order:expire:* key (armed by SetOrderExpiry, cleared by
// ClearOrderExpiry) and returns how many are still live, i.e. orders with an unpaid stock hold.
func (r *redisRepository) GetActiveHoldCount(ctx context.Context) (int64, error) {
	var total int64
	var cursor uint64
	for {
		keys, next, err := r.rdb.Scan(ctx, cursor, "order:expire:*", 100).Result()
		if err != nil {
			return 0, err
		}
		total += int64(len(keys))
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return total, nil
}

func seatStatusKey(eventID string) string {
	return fmt.Sprintf("event:%s:seat_status", eventID)
}

func seatHoldKey(eventID, seatID string) string {
	return fmt.Sprintf("hold:seat:%s:%s", eventID, seatID)
}

func (r *redisRepository) ReserveSpecificSeat(ctx context.Context, eventID, seatID, userID string, ttlSeconds int) (bool, error) {
	res, err := r.rdb.EvalSha(ctx, r.reserveSeatLuaSHA,
		[]string{seatStatusKey(eventID), seatHoldKey(eventID, seatID)},
		seatID, userID, ttlSeconds,
	).Result()
	if err != nil {
		return false, err
	}
	code, _ := res.(int64)
	return code == 1, nil
}

// GetSeatStatuses reads event:{eventID}:seat_status and, for every field still marked
// HELD:<userID>, verifies the corresponding hold:seat:{eventID}:{seatID} key hasn't expired
// (a Redis hash field carries no TTL of its own, so a fired hold otherwise leaves the seat
// stuck HELD forever). Stale entries are cleared from the hash and reported as absent
// (callers treat a missing seat as AVAILABLE).
func (r *redisRepository) GetSeatStatuses(ctx context.Context, eventID string) (map[string]string, error) {
	hashKey := seatStatusKey(eventID)
	raw, err := r.rdb.HGetAll(ctx, hashKey).Result()
	if err != nil {
		return nil, err
	}

	statuses := make(map[string]string, len(raw))
	held := make([]string, 0, len(raw))
	for seatID, value := range raw {
		if strings.HasPrefix(value, "HELD:") {
			held = append(held, seatID)
			continue
		}
		statuses[seatID] = value
	}
	if len(held) == 0 {
		return statuses, nil
	}

	pipe := r.rdb.Pipeline()
	cmds := make(map[string]*redis.IntCmd, len(held))
	for _, seatID := range held {
		cmds[seatID] = pipe.Exists(ctx, seatHoldKey(eventID, seatID))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}

	stale := make([]string, 0)
	for seatID, cmd := range cmds {
		if cmd.Val() > 0 {
			statuses[seatID] = "HELD"
		} else {
			stale = append(stale, seatID)
		}
	}

	if len(stale) > 0 {
		delPipe := r.rdb.Pipeline()
		for _, seatID := range stale {
			delPipe.HDel(ctx, hashKey, seatID)
		}
		_, _ = delPipe.Exec(ctx)
	}

	return statuses, nil
}

const (
	queueReleaseRateKey = "admin:queue:release_rate"
	queuePausedKey      = "admin:queue:paused"
)

func (r *redisRepository) GetQueueReleaseRate(ctx context.Context, defaultRate int) (int, error) {
	val, err := r.rdb.Get(ctx, queueReleaseRateKey).Int()
	if err != nil {
		if err == redis.Nil {
			return defaultRate, nil
		}
		return 0, err
	}
	return val, nil
}

func (r *redisRepository) SetQueueReleaseRate(ctx context.Context, rate int) error {
	return r.rdb.Set(ctx, queueReleaseRateKey, rate, 0).Err()
}

func (r *redisRepository) IsQueuePaused(ctx context.Context) (bool, error) {
	val, err := r.rdb.Get(ctx, queuePausedKey).Result()
	if err != nil {
		if err == redis.Nil {
			return false, nil
		}
		return false, err
	}
	return val == "1", nil
}

func (r *redisRepository) SetQueuePaused(ctx context.Context, paused bool) error {
	val := "0"
	if paused {
		val = "1"
	}
	return r.rdb.Set(ctx, queuePausedKey, val, 0).Err()
}

// FlushAllQueues scans for every event:*:queue ZSET, sums their cardinalities before deleting
// them, and returns that count so the caller can report how many queued users were dropped.
func (r *redisRepository) FlushAllQueues(ctx context.Context) (int64, error) {
	var flushed int64
	var cursor uint64
	for {
		keys, next, err := r.rdb.Scan(ctx, cursor, "event:*:queue", 100).Result()
		if err != nil {
			return flushed, err
		}
		for _, key := range keys {
			count, err := r.rdb.ZCard(ctx, key).Result()
			if err != nil {
				return flushed, err
			}
			if err := r.rdb.Del(ctx, key).Err(); err != nil {
				return flushed, err
			}
			flushed += count
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return flushed, nil
}
