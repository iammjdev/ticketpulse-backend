package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/iammjdev/ticketpulse-backend/internal/config"
	"github.com/iammjdev/ticketpulse-backend/internal/event"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
	"github.com/iammjdev/ticketpulse-backend/internal/service"
)

// NotificationWorker consumes ticketpulse.order.paid and dispatches the e-ticket confirmation
// email (or logs it, if SMTP isn't configured). It re-fetches the order/zone/user from
// Postgres rather than trusting the Kafka payload as final — the payload only needs to carry
// enough to route the event; the DB is the source of truth for what actually gets emailed.
type NotificationWorker struct {
	reader *kafka.Reader
	orders repository.OrderRepository
	events repository.EventRepository
	users  repository.UserRepository
	smtp   config.SMTPConfig
}

func NewNotificationWorker(
	brokers []string,
	topic string,
	groupID string,
	orders repository.OrderRepository,
	events repository.EventRepository,
	users repository.UserRepository,
	smtpCfg config.SMTPConfig,
) *NotificationWorker {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  brokers,
		Topic:    topic,
		GroupID:  groupID,
		MinBytes: 1,
		MaxBytes: 10e6,
		MaxWait:  500 * time.Millisecond,
		// LastOffset (not FirstOffset): a fresh consumer group should only email new
		// payments, not replay every historical ORDER_PAID event ever published.
		StartOffset:    kafka.LastOffset,
		CommitInterval: time.Second,
	})

	return &NotificationWorker{
		reader: reader,
		orders: orders,
		events: events,
		users:  users,
		smtp:   smtpCfg,
	}
}

func (w *NotificationWorker) Start(ctx context.Context) {
	log.Println("📬 Notification Worker initialized and listening for ORDER_PAID events...")

	for {
		select {
		case <-ctx.Done():
			log.Println("🛑 Stopping Notification Worker...")
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
				log.Printf("⚠️ Notification Worker Kafka read error (is Kafka running?): %v\n", err)
				time.Sleep(1 * time.Second)
				continue
			}

			var evt event.OrderPaidEvent
			if err := json.Unmarshal(msg.Value, &evt); err != nil {
				log.Printf("❌ Failed to unmarshal ORDER_PAID event: %v\n", err)
				continue
			}

			// Ignore the dummy pre-warm event (see main.go) published solely to force the
			// topic into existence before this consumer group's first read.
			if evt.OrderID == "" {
				continue
			}

			if err := w.handleEvent(ctx, evt); err != nil {
				log.Printf("❌ Failed to process ORDER_PAID event for order %s: %v\n", evt.OrderID, err)
			}
		}
	}
}

func (w *NotificationWorker) handleEvent(ctx context.Context, evt event.OrderPaidEvent) error {
	order, err := w.orders.FindByID(ctx, evt.OrderID)
	if err != nil {
		return fmt.Errorf("order lookup failed: %w", err)
	}

	recipient := evt.Email
	recipientName := "there"
	if u, err := w.users.FindByID(ctx, order.UserID); err == nil {
		if recipient == "" {
			recipient = u.Email
		}
		if u.FullName != "" {
			recipientName = u.FullName
		}
	}
	if recipient == "" {
		return fmt.Errorf("no recipient email on file for user %s", order.UserID)
	}

	zoneName := order.ZoneID
	if name, err := w.events.FindZoneName(ctx, order.ZoneID); err == nil && name != "" {
		zoneName = name
	}

	signature := service.SignTicketID(order.ID)
	data := TicketEmailData{
		RecipientName: recipientName,
		OrderID:       order.ID,
		EventTitle:    order.EventTitle,
		VenueName:     order.VenueName,
		EventDate:     order.EventDate.Format("Mon, 02 Jan 2006 • 15:04 MST"),
		ZoneName:      zoneName,
		Quantity:      order.Quantity,
		TotalAmount:   fmt.Sprintf("%.2f", order.TotalAmount),
		Signature:     signature,
	}
	data.QRCodeURL = buildTicketQRCodeURL(order.ID, signature)

	subject, htmlBody, err := RenderETicketEmail(data)
	if err != nil {
		return fmt.Errorf("render failed: %w", err)
	}

	if err := SendETicketEmail(w.smtp, recipient, subject, htmlBody, data); err != nil {
		return fmt.Errorf("send failed: %w", err)
	}

	log.Printf("✅ E-ticket notification dispatched for order %s -> %s\n", order.ID, recipient)
	return nil
}

// buildTicketQRCodeURL mirrors the QR rendering approach already used for PromptPay charges
// (see payment_service.go) — a hosted QR image generator, since the backend has no barcode
// library of its own. The payload matches the gate scanner's expected shape (ticket_id/sig).
func buildTicketQRCodeURL(ticketID, signature string) string {
	payload := fmt.Sprintf(`{"ticket_id":"%s","sig":"%s"}`, ticketID, signature)
	return "https://api.qrserver.com/v1/create-qr-code/?size=300x300&data=" + url.QueryEscape(payload)
}
