package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/segmentio/kafka-go"
)

type OrderCreatedEvent struct {
	OrderID     string    `json:"order_id"`
	UserID      string    `json:"user_id"`
	EventID     string    `json:"event_id"`
	ZoneID      string    `json:"zone_id"`
	Quantity    int       `json:"quantity"`
	TotalAmount float64   `json:"total_amount"`
	Price       float64   `json:"price"`
	Timestamp   time.Time `json:"timestamp"`
}

type KafkaProducer struct {
	writer *kafka.Writer
}

func NewKafkaProducer(brokerAddr, topic string) *KafkaProducer {
	writer := &kafka.Writer{
		Addr:     kafka.TCP(brokerAddr),
		Topic:    topic,
		Balancer: &kafka.LeastBytes{},
	}
	return &KafkaProducer{writer: writer}
}

// PublishOrderCreated publishes an order event payload to Kafka topic
func (p *KafkaProducer) PublishOrderCreated(ctx context.Context, event OrderCreatedEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal order event: %w", err)
	}

	msg := kafka.Message{
		Key:   []byte(event.OrderID),
		Value: payload,
	}

	if err := p.writer.WriteMessages(ctx, msg); err != nil {
		return fmt.Errorf("failed to write message to kafka: %w", err)
	}

	log.Printf("📢 Published OrderCreatedEvent to Kafka: OrderID=%s", event.OrderID)
	return nil
}

func (p *KafkaProducer) Close() error {
	return p.writer.Close()
}
