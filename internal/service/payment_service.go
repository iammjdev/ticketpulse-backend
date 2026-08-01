package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
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
	orders repository.OrderRepository
	redis  repository.RedisRepository
}

func NewPaymentService(orders repository.OrderRepository, redis repository.RedisRepository) *PaymentService {
	return &PaymentService{orders: orders, redis: redis}
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
	}
	return order, nil
}

// OrderExpiryTTL exposes the live Redis countdown for the order-status polling endpoint.
func (s *PaymentService) OrderExpiryTTL(ctx context.Context, orderID string) (time.Duration, error) {
	return s.redis.OrderExpiryTTL(ctx, orderID)
}
