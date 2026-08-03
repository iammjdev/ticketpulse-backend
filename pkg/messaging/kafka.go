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

// WarmupTopic forces kafka-go to dial and auto-create the bound topic without emitting an
// operational log line — used at startup so the first real publish doesn't race topic creation.
func (p *KafkaProducer) WarmupTopic(ctx context.Context) error {
	return p.writer.WriteMessages(ctx, kafka.Message{Value: []byte("{}")})
}

func (p *KafkaProducer) Close() error {
	return p.writer.Close()
}

// ConsumerLag reports how many messages a consumer group has yet to read on a topic: the sum,
// across every partition, of (high watermark - last committed offset). Used by the admin
// dashboard's system-health panel. Returns an error if the broker is unreachable so the caller
// can report a degraded status instead of a fabricated number.
func ConsumerLag(ctx context.Context, brokerAddr, topic, groupID string) (int64, error) {
	conn, err := kafka.DialContext(ctx, "tcp", brokerAddr)
	if err != nil {
		return 0, fmt.Errorf("dial kafka broker: %w", err)
	}
	defer conn.Close()

	partitions, err := conn.ReadPartitions(topic)
	if err != nil {
		return 0, fmt.Errorf("read partitions for topic %s: %w", topic, err)
	}
	if len(partitions) == 0 {
		return 0, nil
	}

	partitionIDs := make([]int, len(partitions))
	offsetReqs := make([]kafka.OffsetRequest, len(partitions))
	for i, p := range partitions {
		partitionIDs[i] = p.ID
		offsetReqs[i] = kafka.LastOffsetOf(p.ID)
	}

	client := &kafka.Client{Addr: kafka.TCP(brokerAddr), Timeout: 5 * time.Second}

	listResp, err := client.ListOffsets(ctx, &kafka.ListOffsetsRequest{
		Topics: map[string][]kafka.OffsetRequest{topic: offsetReqs},
	})
	if err != nil {
		return 0, fmt.Errorf("list offsets: %w", err)
	}

	fetchResp, err := client.OffsetFetch(ctx, &kafka.OffsetFetchRequest{
		GroupID: groupID,
		Topics:  map[string][]int{topic: partitionIDs},
	})
	if err != nil {
		return 0, fmt.Errorf("fetch consumer group offsets: %w", err)
	}

	committed := make(map[int]int64, len(partitionIDs))
	for _, part := range fetchResp.Topics[topic] {
		committed[part.Partition] = part.CommittedOffset
	}

	var lag int64
	for _, po := range listResp.Topics[topic] {
		c, ok := committed[po.Partition]
		if !ok || c < 0 {
			c = 0
		}
		if diff := po.LastOffset - c; diff > 0 {
			lag += diff
		}
	}
	return lag, nil
}
