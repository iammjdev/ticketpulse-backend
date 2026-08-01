package worker

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"

	"github.com/iammjdev/ticketpulse-backend/internal/repository"
)

// OrderExpirySeconds is the payment window granted to a freshly reserved order. It governs
// both the orders.expires_at column and the Redis order:expire:{id} TTL that the
// ExpirationWorker actually watches — the two must stay in lockstep.
const OrderExpirySeconds = 10 * time.Minute

type OrderCreatedEvent struct {
	OrderID   string    `json:"order_id"`
	EventID   string    `json:"event_id"`
	ZoneID    string    `json:"zone_id"`
	UserID    string    `json:"user_id"`
	Quantity  int       `json:"quantity"`
	Price     float64   `json:"price"`
	Timestamp time.Time `json:"timestamp"`
}

type OrderWorker struct {
	reader    *kafka.Reader
	db        *pgxpool.Pool
	redisRepo repository.RedisRepository
}

func NewOrderWorker(brokers []string, topic string, groupID string, db *pgxpool.Pool, redisRepo repository.RedisRepository) *OrderWorker {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        brokers,
		Topic:          topic,
		GroupID:        groupID,
		MinBytes:       1,
		MaxBytes:       10e6,
		MaxWait:        500 * time.Millisecond,
		StartOffset:    kafka.FirstOffset,
		CommitInterval: time.Second,
	})

	return &OrderWorker{
		reader:    reader,
		db:        db,
		redisRepo: redisRepo,
	}
}

func (w *OrderWorker) Start(ctx context.Context) {
	log.Println("⚙️ Order Processing Worker initialized and listening to Kafka topic...")

	for {
		select {
		case <-ctx.Done():
			log.Println("🛑 Stopping Order Processing Worker...")
			if err := w.reader.Close(); err != nil {
				log.Printf("⚠️ Error closing Kafka reader: %v\n", err)
			}
			return
		default:
			msg, err := w.reader.ReadMessage(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("⚠️ Kafka Consumer Error: %v\n", err)
				time.Sleep(1 * time.Second)
				continue
			}

			var event OrderCreatedEvent
			if err := json.Unmarshal(msg.Value, &event); err != nil {
				log.Printf("❌ Failed to unmarshal message: %v\n", err)
				continue
			}

			// Ignore dummy pre-warm events
			if event.Quantity == 0 {
				continue
			}

			if err := w.persistOrderToDB(ctx, &event); err != nil {
				log.Printf("❌ Failed to persist order %s to DB: %v\n", event.OrderID, err)
				continue
			}
			log.Printf("✅ Order %s persisted as PENDING — awaiting payment\n", event.OrderID)

			// Arms the timer the ExpirationWorker actually watches; the orders.expires_at
			// column set alongside it is informational only.
			if err := w.redisRepo.SetOrderExpiry(ctx, event.OrderID, OrderExpirySeconds); err != nil {
				log.Printf("⚠️ Failed to arm Redis expiry for order %s: %v\n", event.OrderID, err)
			}
		}
	}
}

func (w *OrderWorker) persistOrderToDB(ctx context.Context, event *OrderCreatedEvent) error {
	// language=PostgreSQL
	query := `
		INSERT INTO orders (id, user_id, event_id, zone_id, quantity, total_amount, status, idempotency_key, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'PENDING', $7, $8, NOW(), NOW())
	`
	totalAmount := float64(event.Quantity) * event.Price
	idempotencyKey := event.OrderID // Use OrderID as unique idempotency key
	expiresAt := time.Now().Add(OrderExpirySeconds)

	_, err := w.db.Exec(ctx, query, event.OrderID, event.UserID, event.EventID, event.ZoneID, event.Quantity, totalAmount, idempotencyKey, expiresAt)
	if err != nil {
		return err
	}

	return nil
}
