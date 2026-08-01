package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/event"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
)

// ErrOrderNotPayable covers both "order isn't PENDING anymore" and "its Redis expiry window
// already lapsed" — in either case, charging it would let a user pay for tickets that were
// (or are about to be) released back to inventory.
var ErrOrderNotPayable = errors.New("order is not awaiting payment")

type ChargeResult struct {
	ChargeID         string
	QRCodeURL        string
	PromptPayPayload string
	ExpiresAt        time.Time
	Amount           float64
}

// PaymentService simulates a payment-gateway integration: no real acquirer is wired up (there
// are no live gateway credentials in this project), but the shape of the flow — charge
// creation, HMAC-verified webhook confirmation, and Redis-synced expiry — mirrors a real one
// closely enough to demo end-to-end.
type PaymentService struct {
	orders   repository.OrderRepository
	redis    repository.RedisRepository
	users    repository.UserRepository
	notifier *event.Producer
}

func NewPaymentService(orders repository.OrderRepository, redis repository.RedisRepository, users repository.UserRepository, notifier *event.Producer) *PaymentService {
	return &PaymentService{orders: orders, redis: redis, users: users, notifier: notifier}
}

// CreateCharge issues a simulated charge for a pending order. Its expiry is read straight
// from the order's live Redis TTL — the same value the ExpirationWorker watches — so the
// frontend countdown can never drift from the moment cancellation actually fires.
func (s *PaymentService) CreateCharge(ctx context.Context, orderID, method string) (*ChargeResult, error) {
	order, err := s.orders.FindByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.Status != domain.OrderPending {
		return nil, ErrOrderNotPayable
	}

	ttl, err := s.redis.OrderExpiryTTL(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if ttl <= 0 {
		return nil, ErrOrderNotPayable
	}

	chargeID := "chrg_" + uuid.New().String()
	result := &ChargeResult{
		ChargeID:  chargeID,
		ExpiresAt: time.Now().Add(ttl),
		Amount:    order.TotalAmount,
	}

	if method == "PROMPTPAY" {
		payload := buildPromptPayPayload(chargeID, order.TotalAmount)
		result.PromptPayPayload = payload
		result.QRCodeURL = "https://api.qrserver.com/v1/create-qr-code/?size=300x300&data=" + url.QueryEscape(payload)
	}

	return result, nil
}

// buildPromptPayPayload produces an illustrative EMVCo-tag-shaped string for QR rendering.
// It is NOT a real PromptPay merchant payload — there is no live acquirer connected — but the
// tag/length/value structure mirrors the real format closely enough for a demo QR code.
func buildPromptPayPayload(chargeID string, amount float64) string {
	return fmt.Sprintf("00020101021129370016A000000677010111011300660000000005802TH5303764540%.2f6304%s", amount, chargeID)
}

// ConfirmPayment marks a pending order paid and disarms its Redis expiry timer. Shared by the
// HMAC-verified webhook handler and the admin payment-simulator dev tool so both apply
// identical side effects.
func (s *PaymentService) ConfirmPayment(ctx context.Context, orderID string) (*domain.Order, error) {
	order, err := s.orders.MarkPaid(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.Status == domain.OrderCompleted {
		_ = s.redis.ClearOrderExpiry(ctx, orderID)
		s.publishOrderPaid(ctx, order)
	}
	return order, nil
}

// ResendNotification re-publishes ORDER_PAID for an already-paid order, triggering the
// notification worker to send another copy of the e-ticket email. Used by the
// /orders/:id/resend-email endpoint; rate limiting is the caller's responsibility.
func (s *PaymentService) ResendNotification(ctx context.Context, orderID string) (*domain.Order, error) {
	order, err := s.orders.FindByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.Status != domain.OrderCompleted {
		return nil, ErrOrderNotPayable
	}
	s.publishOrderPaid(ctx, order)
	return order, nil
}

// publishOrderPaid best-effort emits the ORDER_PAID Kafka event. Failure (including Kafka
// being offline in dev) is logged and swallowed — email delivery is a downstream concern and
// must never fail the payment confirmation or resend request that triggered it.
func (s *PaymentService) publishOrderPaid(ctx context.Context, order *domain.Order) {
	email := ""
	if u, err := s.users.FindByID(ctx, order.UserID); err == nil {
		email = u.Email
	} else {
		log.Printf("⚠️ Could not resolve email for user %s (order %s): %v\n", order.UserID, order.ID, err)
	}

	evt := event.OrderPaidEvent{
		EventType:  "ORDER_PAID",
		OrderID:    order.ID,
		UserID:     order.UserID,
		Email:      email,
		EventTitle: order.EventTitle,
		Amount:     order.TotalAmount,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.notifier.PublishOrderPaid(ctx, evt); err != nil {
		log.Printf("⚠️ Failed to publish ORDER_PAID event for order %s (Kafka may be offline): %v\n", order.ID, err)
	}
}

// OrderExpiryTTL exposes the live Redis countdown for the order-status polling endpoint.
func (s *PaymentService) OrderExpiryTTL(ctx context.Context, orderID string) (time.Duration, error) {
	return s.redis.OrderExpiryTTL(ctx, orderID)
}
