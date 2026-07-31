package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
	"github.com/iammjdev/ticketpulse-backend/internal/service"
)

type OrderHandler struct {
	orders repository.OrderRepository
	users  repository.UserRepository
}

func NewOrderHandler(orders repository.OrderRepository, users repository.UserRepository) *OrderHandler {
	return &OrderHandler{orders: orders, users: users}
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
