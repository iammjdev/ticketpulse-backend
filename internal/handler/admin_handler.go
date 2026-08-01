package handler

import (
	"github.com/gofiber/fiber/v2"

	"github.com/iammjdev/ticketpulse-backend/internal/repository"
)

type AdminHandler struct {
	orders repository.OrderRepository
	redis  repository.RedisRepository
}

func NewAdminHandler(orders repository.OrderRepository, redis repository.RedisRepository) *AdminHandler {
	return &AdminHandler{orders: orders, redis: redis}
}

// Stats aggregates platform-wide gross revenue and tickets sold from PostgreSQL (paid
// orders) alongside the total active virtual-queue length from Redis. ADMIN only.
func (h *AdminHandler) Stats(c *fiber.Ctx) error {
	revenue, ticketsSold, err := h.orders.AdminStats(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to compute stats"})
	}

	queueLength, err := h.redis.TotalQueueLength(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to compute queue length"})
	}

	return c.JSON(fiber.Map{
		"gross_revenue":       revenue,
		"tickets_sold":        ticketsSold,
		"active_queue_length": queueLength,
	})
}
