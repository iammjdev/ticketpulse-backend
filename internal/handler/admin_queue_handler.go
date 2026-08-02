package handler

import (
	"github.com/gofiber/fiber/v2"

	"github.com/iammjdev/ticketpulse-backend/internal/repository"
)

const defaultQueueReleaseRate = 500

// AdminQueueHandler exposes admin controls over the virtual waiting room: live backlog depth,
// the token-bucket dispatch rate, an emergency pause flag, and a flush-all action. Every value
// is read from and written to Redis for real — nothing here is simulated.
type AdminQueueHandler struct {
	redis repository.RedisRepository
}

func NewAdminQueueHandler(redis repository.RedisRepository) *AdminQueueHandler {
	return &AdminQueueHandler{redis: redis}
}

func (h *AdminQueueHandler) status(c *fiber.Ctx) (fiber.Map, error) {
	backlog, err := h.redis.TotalQueueLength(c.Context())
	if err != nil {
		return nil, err
	}
	rate, err := h.redis.GetQueueReleaseRate(c.Context(), defaultQueueReleaseRate)
	if err != nil {
		return nil, err
	}
	paused, err := h.redis.IsQueuePaused(c.Context())
	if err != nil {
		return nil, err
	}
	return fiber.Map{
		"backlog":      backlog,
		"release_rate": rate,
		"paused":       paused,
	}, nil
}

// Status reports the current backlog depth, release rate, and pause state. ADMIN only.
func (h *AdminQueueHandler) Status(c *fiber.Ctx) error {
	state, err := h.status(c)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to read queue status"})
	}
	return c.JSON(state)
}

// SetRate persists a new token-bucket dispatch rate (100-2000 users/sec). ADMIN only.
func (h *AdminQueueHandler) SetRate(c *fiber.Ctx) error {
	var req struct {
		Rate int `json:"rate"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}
	if req.Rate < 100 || req.Rate > 2000 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "rate must be between 100 and 2000"})
	}
	if err := h.redis.SetQueueReleaseRate(c.Context(), req.Rate); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to persist release rate"})
	}
	state, err := h.status(c)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to read queue status"})
	}
	return c.JSON(state)
}

// SetPaused toggles the emergency dispatch-pause flag. ADMIN only.
func (h *AdminQueueHandler) SetPaused(c *fiber.Ctx) error {
	var req struct {
		Paused bool `json:"paused"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}
	if err := h.redis.SetQueuePaused(c.Context(), req.Paused); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to persist pause state"})
	}
	state, err := h.status(c)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to read queue status"})
	}
	return c.JSON(state)
}

// Flush deletes every event's virtual-queue ZSET and reports how many users were dropped.
// ADMIN only, irreversible.
func (h *AdminQueueHandler) Flush(c *fiber.Ctx) error {
	flushed, err := h.redis.FlushAllQueues(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to flush queues"})
	}
	return c.JSON(fiber.Map{"flushed_count": flushed})
}
