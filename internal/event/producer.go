package event

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/segmentio/kafka-go"
)

// OrderPaidEvent is published to ticketpulse.order.paid whenever an order transitions to
// PAID (COMPLETED), and re-published unchanged by the resend-email endpoint. The notification
// worker treats OrderID as the source of truth and re-fetches the order/user from Postgres
// rather than trusting these fields as final — this payload only needs to carry enough to
// route the event and render a quick log line.
type OrderPaidEvent struct {
	EventType  string  `json:"event_type"`
	OrderID    string  `json:"order_id"`
	UserID     string  `json:"user_id"`
	Email      string  `json:"email"`
	EventTitle string  `json:"event_title"`
	Amount     float64 `json:"amount"`
	Timestamp  string  `json:"timestamp"`
}

// Producer wraps a Kafka writer bound to a single topic. Construction never fails on a
// down/unreachable broker — kafka-go dials lazily on the first WriteMessages call — so a dev
// environment without Kafka running can still boot the API; only the publish call itself
// degrades (logged, non-fatal).
type Producer struct {
	writer *kafka.Writer
}

func NewProducer(brokers []string, topic string) *Producer {
	return &Producer{
		writer: &kafka.Writer{
			Addr:                   kafka.TCP(brokers...),
			Topic:                  topic,
			Balancer:               &kafka.LeastBytes{},
			AllowAutoTopicCreation: true,
			WriteTimeout:           3 * time.Second,
		},
	}
}

// PublishOrderPaid best-effort publishes evt. Failure (including Kafka being offline) is
// returned to the caller to log — it must never block or fail the payment/resend request that
// triggered it, since email delivery is a downstream concern, not a payment-critical one.
func (p *Producer) PublishOrderPaid(ctx context.Context, evt OrderPaidEvent) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("failed to marshal ORDER_PAID event: %w", err)
	}

	writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	msg := kafka.Message{
		Key:   []byte(evt.OrderID),
		Value: payload,
	}
	if err := p.writer.WriteMessages(writeCtx, msg); err != nil {
		return fmt.Errorf("failed to publish ORDER_PAID event to kafka: %w", err)
	}

	log.Printf("📢 Published ORDER_PAID to Kafka: OrderID=%s Email=%s\n", evt.OrderID, evt.Email)
	return nil
}

func (p *Producer) Close() error {
	return p.writer.Close()
}
