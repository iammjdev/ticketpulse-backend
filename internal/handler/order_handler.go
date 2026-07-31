package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
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
	OrderID string `json:"order_id"`
}

// VerifyTicket is the gate-side scan endpoint: validates the order id and flips it to
// CHECKED_IN. Restricted to ADMIN / GATE_STAFF via RequireAnyRole.
func (h *OrderHandler) VerifyTicket(c *fiber.Ctx) error {
	var req verifyTicketRequest
	if err := c.BodyParser(&req); err != nil || req.OrderID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "order_id is required"})
	}

	order, err := h.orders.VerifyAndCheckInTicket(c.Context(), req.OrderID)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrOrderNotFound):
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Order not found"})
		case errors.Is(err, repository.ErrOrderAlreadyIn):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "TICKET_ALREADY_USED", "message": "Ticket has already been checked in"})
		case errors.Is(err, repository.ErrOrderNotEligible):
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "Order is not eligible for check-in"})
		default:
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to verify ticket"})
		}
	}

	result := fiber.Map{"order": orderResponse(order)}
	if holder, err := h.users.FindByID(c.Context(), order.UserID); err == nil {
		result["holder"] = fiber.Map{"full_name": holder.FullName, "email": holder.Email}
	}
	return c.JSON(result)
}
