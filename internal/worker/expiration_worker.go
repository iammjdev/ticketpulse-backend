package worker

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/iammjdev/ticketpulse-backend/internal/repository"
	"github.com/redis/go-redis/v9"
)

type ExpirationWorker struct {
	redisClient *redis.Client
	redisRepo   repository.RedisRepository
}

func NewExpirationWorker(redisClient *redis.Client, redisRepo repository.RedisRepository) *ExpirationWorker {
	return &ExpirationWorker{
		redisClient: redisClient,
		redisRepo:   redisRepo,
	}
}

// Start listens to Redis Keyspace Expiration events and releases expired seat holds
func (w *ExpirationWorker) Start(ctx context.Context) {
	// Enable Keyspace Events in Redis config (Expired events on keys)
	if err := w.redisClient.ConfigSet(ctx, "notify-keyspace-events", "Ex").Err(); err != nil {
		log.Printf("⚠️ Unable to enable Redis keyspace notifications: %v\n", err)
	}

	// Subscribe to Redis Key Expiration Channel on database 0
	pubsub := w.redisClient.Subscribe(ctx, "__keyevent@0__:expired")
	defer pubsub.Close()

	log.Println("⏳ TicketPulse Hold Expiration Worker active (Listening for expired seat holds)...")

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			log.Println("🛑 Hold Expiration Worker shutting down...")
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			// Key pattern expected: hold:{event_id}:{zone_id}:{quantity}:{user_id}
			key := msg.Payload
			if strings.HasPrefix(key, "hold:") {
				go w.handleExpiredHold(ctx, key)
			}
		}
	}
}

func (w *ExpirationWorker) handleExpiredHold(ctx context.Context, key string) {
	parts := strings.Split(key, ":")
	if len(parts) < 5 {
		return
	}

	eventID := parts[1]
	zoneID := parts[2]
	quantity, err := strconv.Atoi(parts[3])
	if err != nil {
		quantity = 1
	}
	userID := parts[4]

	log.Printf("⏰ HOLD EXPIRED for User %s (Event: %s, Zone: %s). Releasing %d ticket(s)...", userID, eventID, zoneID, quantity)

	// Increment ticket stock back into Redis inventory
	stockKey := fmt.Sprintf("stock:%s:%s", eventID, zoneID)
	newStock, err := w.redisClient.IncrBy(ctx, stockKey, int64(quantity)).Result()
	if err != nil {
		log.Printf("❌ Failed to restore stock in Redis for key %s: %v\n", stockKey, err)
		return
	}

	log.Printf("✅ Restored stock successfully! Key %s stock updated to %d\n", stockKey, newStock)
}
