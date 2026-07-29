package handler

import (
	"bufio"
	"context"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
	"github.com/valyala/fasthttp"
)

type QueueHandler struct {
	redisRepo repository.RedisRepository
}

func NewQueueHandler(redisRepo repository.RedisRepository) *QueueHandler {
	return &QueueHandler{redisRepo: redisRepo}
}

// JoinQueue handles user requests to enter the event queue
func (h *QueueHandler) JoinQueue(c *fiber.Ctx) error {
	type QueueRequest struct {
		EventID string `json:"event_id"`
		UserID  string `json:"user_id"`
	}

	var req QueueRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid payload"})
	}

	position, err := h.redisRepo.EnqueueUser(c.Context(), req.EventID, req.UserID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message":      "Successfully joined the queue!",
		"event_id":     req.EventID,
		"user_id":      req.UserID,
		"queue_number": position,
	})
}

// StreamQueueStatus establishes an HTTP Server-Sent Events (SSE) connection to stream queue positions in real-time
func (h *QueueHandler) StreamQueueStatus(c *fiber.Ctx) error {
	eventID := c.Query("event_id")
	userID := c.Query("user_id")

	if eventID == "" || userID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Missing event_id or user_id"})
	}

	// Set HTTP response headers for Server-Sent Events (SSE)
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("Transfer-Encoding", "chunked")

	// Use FastHTTP StreamWriter without referencing fiber.Ctx directly inside the goroutine
	c.Context().SetBodyStreamWriter(fasthttp.StreamWriter(func(w *bufio.Writer) {
		bgCtx := context.Background()

		for {
			// Query queue position from Redis using isolated background context
			position, err := h.redisRepo.GetQueuePosition(bgCtx, eventID, userID)
			if err != nil {
				fmt.Fprintf(w, "event: error\ndata: {\"error\": \"%s\"}\n\n", err.Error())
				_ = w.Flush()
				break
			}

			// Construct SSE event message payload
			data := fmt.Sprintf("{\"user_id\": \"%s\", \"queue_position\": %d, \"timestamp\": \"%s\"}",
				userID, position, time.Now().Format(time.RFC3339))

			// Write SSE formatted stream payload
			_, writeErr := fmt.Fprintf(w, "data: %s\n\n", data)
			if writeErr != nil {
				// Break loop if client disconnected
				break
			}

			// Flush buffer to force transmission over network
			if flushErr := w.Flush(); flushErr != nil {
				// Connection reset or closed by client
				break
			}

			// Notify client and terminate stream when position reaches 1
			if position == 1 {
				fmt.Fprintf(w, "event: ready\ndata: {\"message\": \"It's your turn! Proceed to checkout.\"}\n\n")
				_ = w.Flush()
				break
			}

			// Interval between queue updates
			time.Sleep(2 * time.Second)
		}
	}))

	return nil
}
