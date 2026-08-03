package config

import (
	"os"
	"strings"
)

// OrderPaidTopic is the Kafka topic the payment service publishes to on PAID transitions,
// and the notification worker consumes from.
const OrderPaidTopic = "ticketpulse.order.paid"

// PasswordResetTopic is the Kafka topic the admin-triggered password reset handler publishes
// to, and the password reset worker consumes from.
const PasswordResetTopic = "ticketpulse.user.password_reset"

type KafkaConfig struct {
	Brokers            []string
	OrderPaidTopic     string
	PasswordResetTopic string
}

// LoadKafkaConfig reads KAFKA_BROKERS (comma-separated, default localhost:9092). Kafka being
// offline at load time is not an error here — kafka-go connects lazily on first read/write, so
// startup always succeeds and individual publish/consume calls degrade gracefully instead.
func LoadKafkaConfig() KafkaConfig {
	raw := os.Getenv("KAFKA_BROKERS")
	if raw == "" {
		raw = "localhost:9092"
	}

	brokers := make([]string, 0)
	for b := range strings.SplitSeq(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			brokers = append(brokers, b)
		}
	}
	if len(brokers) == 0 {
		brokers = []string{"localhost:9092"}
	}

	return KafkaConfig{
		Brokers:            brokers,
		OrderPaidTopic:     OrderPaidTopic,
		PasswordResetTopic: PasswordResetTopic,
	}
}
