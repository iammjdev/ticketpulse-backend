package worker

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/iammjdev/ticketpulse-backend/internal/config"
	"github.com/iammjdev/ticketpulse-backend/internal/event"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
)

// PasswordResetTTLMinutes mirrors the Redis TTL set on password_reset:{userID} by the admin
// trigger handler — used only to render "expires in N minutes" in the email copy.
const PasswordResetTTLMinutes = 15

// PasswordResetWorker consumes ticketpulse.user.password_reset and dispatches the reset
// notice email (or logs it, if SMTP isn't configured). Re-fetches the user from Postgres
// rather than trusting the Kafka payload as final, same pattern as NotificationWorker.
type PasswordResetWorker struct {
	reader *kafka.Reader
	users  repository.UserRepository
	smtp   config.SMTPConfig
}

func NewPasswordResetWorker(
	brokers []string,
	topic string,
	groupID string,
	users repository.UserRepository,
	smtpCfg config.SMTPConfig,
) *PasswordResetWorker {
	// Unlike ticketpulse.order.paid (which has existed since this project's earliest test
	// runs), this topic is brand new — a kafka-go group reader that starts polling before the
	// topic exists never self-heals once the topic is lazily auto-created by a later producer
	// write; it silently sits on zero partitions forever. Explicitly creating the topic here
	// (idempotent — "already exists" is expected and ignored) guarantees it's there before the
	// reader ever attempts group coordination.
	ensureTopicExists(brokers, topic)

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        brokers,
		Topic:          topic,
		GroupID:        groupID,
		MinBytes:       1,
		MaxBytes:       10e6,
		MaxWait:        500 * time.Millisecond,
		StartOffset:    kafka.LastOffset,
		CommitInterval: time.Second,
	})

	return &PasswordResetWorker{reader: reader, users: users, smtp: smtpCfg}
}

// ensureTopicExists idempotently creates topic with a single partition if it doesn't already
// exist. Best-effort: logged, non-fatal on failure (e.g. Kafka offline at startup) — the reader
// will simply retry via its normal read-error path in that case.
func ensureTopicExists(brokers []string, topic string) {
	if len(brokers) == 0 {
		return
	}
	conn, err := kafka.Dial("tcp", brokers[0])
	if err != nil {
		log.Printf("⚠️ Could not dial Kafka to pre-create topic %s: %v\n", topic, err)
		return
	}
	defer conn.Close()

	if err := conn.CreateTopics(kafka.TopicConfig{
		Topic:             topic,
		NumPartitions:     1,
		ReplicationFactor: 1,
	}); err != nil {
		log.Printf("⚠️ Could not pre-create Kafka topic %s (may already exist): %v\n", topic, err)
	}
}

func (w *PasswordResetWorker) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			log.Println("🛑 Stopping Password Reset Worker...")
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
				log.Printf("⚠️ Password Reset Worker Kafka read error (is Kafka running?): %v\n", err)
				time.Sleep(1 * time.Second)
				continue
			}

			var evt event.PasswordResetEvent
			if err := json.Unmarshal(msg.Value, &evt); err != nil {
				log.Printf("❌ Failed to unmarshal PASSWORD_RESET event: %v\n", err)
				continue
			}

			// Ignore the dummy pre-warm event published solely to force the topic into
			// existence before this consumer group's first read.
			if evt.UserID == "" {
				continue
			}

			if err := w.handleEvent(ctx, evt); err != nil {
				log.Printf("❌ Failed to process PASSWORD_RESET event for user %s: %v\n", evt.UserID, err)
			}
		}
	}
}

func (w *PasswordResetWorker) handleEvent(ctx context.Context, evt event.PasswordResetEvent) error {
	recipient := evt.Email
	recipientName := "there"
	if u, err := w.users.FindByID(ctx, evt.UserID); err == nil {
		if recipient == "" {
			recipient = u.Email
		}
		if u.FullName != "" {
			recipientName = u.FullName
		}
	}
	if recipient == "" {
		return nil
	}

	data := PasswordResetEmailData{
		RecipientName: recipientName,
		Token:         evt.Token,
		ExpiresInMins: PasswordResetTTLMinutes,
	}

	subject, htmlBody, err := RenderPasswordResetEmail(data)
	if err != nil {
		return err
	}

	if err := SendPasswordResetEmail(w.smtp, recipient, subject, htmlBody, data); err != nil {
		return err
	}

	log.Printf("✅ Password reset notification dispatched for user %s -> %s\n", evt.UserID, recipient)
	return nil
}
