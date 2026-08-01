package handler

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
	"github.com/iammjdev/ticketpulse-backend/internal/service"
)

// resendEmailCooldown is the minimum interval between resend-email requests for a single
// order, enforced via Redis SETNX so it holds across multiple API instances.
const resendEmailCooldown = 60 * time.Second

type OrderHandler struct {
	orders   repository.OrderRepository
	users    repository.UserRepository
	redis    repository.RedisRepository
	payments *service.PaymentService
}

func NewOrderHandler(orders repository.OrderRepository, users repository.UserRepository, redis repository.RedisRepository, payments *service.PaymentService) *OrderHandler {
	return &OrderHandler{orders: orders, users: users, redis: redis, payments: payments}
}

func orderResponse(o *domain.Order) fiber.Map {
	return fiber.Map{
		"id":            o.ID,
		"event_id":      o.EventID,
		"event_title":   o.EventTitle,
		"venue_name":    o.VenueName,
		"event_date":    o.EventDate,
		"poster_url":    o.PosterURL,
		"zone_id":       o.ZoneID,
		"quantity":      o.Quantity,
		"total_amount":  o.TotalAmount,
		"status":        o.Status,
		"created_at":    o.CreatedAt,
		"checked_in_at": o.CheckedInAt,
		// HMAC-SHA256(order id) — embedded in the QR payload and re-verified at the gate.
		"signature": service.SignTicketID(o.ID),
	}
}

// GetMyOrders returns the authenticated user's real order history from PostgreSQL.
func (h *OrderHandler) GetMyOrders(c *fiber.Ctx) error {
	userID, _ := c.Locals("userId").(string)

	orders, err := h.orders.FindOrdersByUserID(c.Context(), userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch order history"})
	}

	response := make([]fiber.Map, 0, len(orders))
	for _, o := range orders {
		response = append(response, orderResponse(o))
	}
	return c.JSON(fiber.Map{"orders": response})
}

type verifyTicketRequest struct {
	TicketID  string `json:"ticket_id"`
	Signature string `json:"signature"`
}

// VerifyTicket is the gate-side scan endpoint. It authenticates the QR payload's HMAC-SHA256
// signature against ticket_id (no DB round trip needed to detect tampering), then flips the
// matching order to CHECKED_IN. Restricted to ADMIN / GATE_STAFF via RequireAnyRole.
func (h *OrderHandler) VerifyTicket(c *fiber.Ctx) error {
	var req verifyTicketRequest
	if err := c.BodyParser(&req); err != nil || req.TicketID == "" || req.Signature == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "ticket_id and signature are required"})
	}

	if !service.VerifyTicketSignature(req.TicketID, req.Signature) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "INVALID_SIGNATURE", "message": "Ticket signature could not be verified"})
	}

	order, err := h.orders.VerifyAndCheckInTicket(c.Context(), req.TicketID)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrOrderNotFound):
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "TICKET_NOT_FOUND"})
		case errors.Is(err, repository.ErrOrderAlreadyIn):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "TICKET_ALREADY_USED", "checked_in_at": order.CheckedInAt})
		case errors.Is(err, repository.ErrOrderCancelled):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "TICKET_CANCELLED"})
		case errors.Is(err, repository.ErrOrderNotEligible):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "TICKET_NOT_ELIGIBLE"})
		default:
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to verify ticket"})
		}
	}

	holderName := ""
	if holder, err := h.users.FindByID(c.Context(), order.UserID); err == nil {
		holderName = holder.FullName
	}

	return c.JSON(fiber.Map{
		"ticket_id":     order.ID,
		"user_name":     holderName,
		"event_title":   order.EventTitle,
		"zone_name":     order.ZoneID,
		"checked_in_at": order.CheckedInAt,
		"status":        order.Status,
	})
}

// ResendEmail re-triggers the e-ticket notification for a PAID order by re-publishing
// ORDER_PAID to Kafka. Requires the caller to own the order (or be ADMIN) and be PAID
// (COMPLETED); rate limited to 1 request/60s/order via Redis SETNX.
func (h *OrderHandler) ResendEmail(c *fiber.Ctx) error {
	orderID := c.Params("id")

	order, err := h.orders.FindByID(c.Context(), orderID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Order not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch order"})
	}
	if !ownsOrder(c, order) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Not your order"})
	}
	if order.Status != domain.OrderCompleted {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "Order is not paid yet — nothing to resend"})
	}

	allowed, err := h.redis.TryAcquireRateLimit(c.Context(), "resend_email:"+orderID, resendEmailCooldown)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to check rate limit"})
	}
	if !allowed {
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": "Please wait a minute before requesting another resend"})
	}

	if _, err := h.payments.ResendNotification(c.Context(), orderID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to queue e-ticket resend"})
	}

	return c.JSON(fiber.Map{"message": "E-ticket email resend queued", "order_id": orderID})
}
