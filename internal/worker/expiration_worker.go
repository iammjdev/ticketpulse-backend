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
	orders      repository.OrderRepository
}

func NewExpirationWorker(redisClient *redis.Client, redisRepo repository.RedisRepository, orders repository.OrderRepository) *ExpirationWorker {
	return &ExpirationWorker{
		redisClient: redisClient,
		redisRepo:   redisRepo,
		orders:      orders,
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

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			log.Println("🛑 Expiration Worker shutting down...")
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			key := msg.Payload
			switch {
			case strings.HasPrefix(key, "hold:"):
				// Key pattern: hold:{event_id}:{zone_id}:{quantity}:{user_id}
				go w.handleExpiredHold(ctx, key)
			case strings.HasPrefix(key, "order:expire:"):
				go w.handleExpiredOrder(ctx, key)
			}
		}
	}
}

// handleExpiredOrder fires when an order:expire:{order_id} TTL key lapses — meaning the user
// never completed payment within the 10-minute window. It cancels the order (only if it's
// still PENDING; a paid order's key was already deleted by ConfirmPayment, so this is a
// best-effort backstop, not the primary path) and restores the reserved seats to Redis stock.
func (w *ExpirationWorker) handleExpiredOrder(ctx context.Context, key string) {
	orderID := strings.TrimPrefix(key, "order:expire:")

	order, wasCancelled, err := w.orders.CancelIfPending(ctx, orderID)
	if err != nil {
		log.Printf("❌ Failed to cancel expired order %s: %v\n", orderID, err)
		return
	}
	if !wasCancelled {
		// Already paid (or already cancelled by a prior delivery) — nothing to restore.
		return
	}

	if err := w.redisRepo.RestoreZoneStock(ctx, order.EventID, order.ZoneID, order.Quantity); err != nil {
		log.Printf("❌ Failed to restore stock for expired order %s: %v\n", orderID, err)
		return
	}

	log.Printf("⏰ ORDER EXPIRED unpaid: %s. Cancelled and restored %d ticket(s) to zone %s\n", orderID, order.Quantity, order.ZoneID)
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
