package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
	"github.com/iammjdev/ticketpulse-backend/internal/service"
)

type PaymentHandler struct {
	payments      *service.PaymentService
	orders        repository.OrderRepository
	webhookSecret string
}

func NewPaymentHandler(payments *service.PaymentService, orders repository.OrderRepository, webhookSecret string) *PaymentHandler {
	return &PaymentHandler{payments: payments, orders: orders, webhookSecret: webhookSecret}
}

// ownsOrder reports whether the authenticated caller may read/act on this order — either
// they placed it, or they're an ADMIN. Guards against IDOR on the order-status/charge routes.
func ownsOrder(c *fiber.Ctx, order *domain.Order) bool {
	userID, _ := c.Locals("userId").(string)
	role, _ := c.Locals("userRole").(string)
	return order.UserID == userID || role == string(domain.RoleAdmin)
}

type chargeRequest struct {
	OrderID       string `json:"order_id"`
	PaymentMethod string `json:"payment_method"`
}

// CreateCharge simulates initiating a gateway charge for a pending order: a PromptPay QR
// payload for PROMPTPAY, or a bare charge id for CREDIT_CARD. Requires the caller to own the
// order (or be ADMIN).
func (h *PaymentHandler) CreateCharge(c *fiber.Ctx) error {
	var req chargeRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	method := strings.ToUpper(strings.TrimSpace(req.PaymentMethod))
	if method != "PROMPTPAY" && method != "CREDIT_CARD" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "payment_method must be PROMPTPAY or CREDIT_CARD"})
	}

	order, err := h.orders.FindByID(c.Context(), req.OrderID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Order not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch order"})
	}
	if !ownsOrder(c, order) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Not your order"})
	}

	charge, err := h.payments.CreateCharge(c.Context(), req.OrderID, method)
	if err != nil {
		if errors.Is(err, service.ErrOrderNotPayable) {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "Order is not awaiting payment"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create charge"})
	}

	return c.JSON(fiber.Map{
		"charge_id":         charge.ChargeID,
		"qr_code_url":       charge.QRCodeURL,
		"promptpay_payload": charge.PromptPayPayload,
		"expires_at":        charge.ExpiresAt,
		"amount":            charge.Amount,
	})
}

type webhookPayload struct {
	Event    string `json:"event"`
	OrderID  string `json:"order_id"`
	ChargeID string `json:"charge_id"`
}

var paidWebhookEvents = map[string]bool{
	"charge.complete": true,
	"payment.success": true,
}

// HandleWebhook verifies the gateway's HMAC-SHA256 signature over the RAW request body — it
// must run before any parsing that could reorder/reserialize fields, since the signature is
// computed over exact bytes. Always acks 200 once the signature checks out (standard webhook
// convention) so the gateway doesn't retry-storm us over business states it doesn't need to
// know about, such as an order that already expired.
func (h *PaymentHandler) HandleWebhook(c *fiber.Ctx) error {
	body := c.Body()
	signature := c.Get("X-Webhook-Signature")
	if signature == "" || !validSignature(h.webhookSecret, body, signature) {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid signature"})
	}

	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil || payload.OrderID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid payload"})
	}

	if paidWebhookEvents[payload.Event] {
		order, err := h.payments.ConfirmPayment(c.Context(), payload.OrderID)
		if err != nil {
			log.Printf("⚠️ Webhook: failed to confirm payment for order %s: %v\n", payload.OrderID, err)
		} else {
			log.Printf("💳 Payment confirmed for order %s (charge %s, status now %s) — notify downstream systems\n", order.ID, payload.ChargeID, order.Status)
		}
	}

	return c.JSON(fiber.Map{"received": true})
}

func validSignature(secret string, body []byte, signature string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(strings.ToLower(strings.TrimSpace(signature))))
}

// GetOrderStatus is the checkout page's 3-second poll target: current status plus seconds
// remaining on the live Redis expiry timer (0 once paid/cancelled/expired).
func (h *PaymentHandler) GetOrderStatus(c *fiber.Ctx) error {
	id := c.Params("id")
	order, err := h.orders.FindByID(c.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Order not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch order"})
	}
	if !ownsOrder(c, order) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Not your order"})
	}

	expiresIn := 0
	if order.Status == domain.OrderPending {
		if ttl, err := h.payments.OrderExpiryTTL(c.Context(), id); err == nil {
			expiresIn = int(ttl.Seconds())
		}
	}

	return c.JSON(fiber.Map{
		"order_id":           order.ID,
		"status":             order.Status,
		"expires_in_seconds": expiresIn,
	})
}

// SimulatePayment applies the exact same success side effects as a verified webhook call,
// without exposing PAYMENT_WEBHOOK_SECRET to the frontend. Backs the admin
// /admin/payment-simulator dev tool. ADMIN only.
func (h *PaymentHandler) SimulatePayment(c *fiber.Ctx) error {
	orderID := c.Params("orderId")
	order, err := h.payments.ConfirmPayment(c.Context(), orderID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Order not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to simulate payment"})
	}
	return c.JSON(fiber.Map{"order_id": order.ID, "status": order.Status})
}
