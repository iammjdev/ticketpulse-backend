package handler

import (
	"bufio"
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

// JoinQueue: API for users to join the ticket queue
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

// StreamQueueStatus: SSE Endpoint to stream real-time queue status to the Frontend
func (h *QueueHandler) StreamQueueStatus(c *fiber.Ctx) error {
	eventID := c.Query("event_id")
	userID := c.Query("user_id")

	if eventID == "" || userID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Missing event_id or user_id"})
	}

	// Set HTTP Headers for Server-Sent Events (SSE)
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("Transfer-Encoding", "chunked")

	// Stream real-time queue data back to the client every 2 seconds
	c.Context().SetBodyStreamWriter(fasthttp.StreamWriter(func(w *bufio.Writer) {
		for {
			ctx := c.Context()
			position, err := h.redisRepo.GetQueuePosition(ctx, eventID, userID)
			if err != nil {
				fmt.Fprintf(w, "event: error\ndata: {\"error\": \"%s\"}\n\n", err.Error())
				w.Flush()
				break
			}

			// Send queue data event in SSE Format (`data: {...}\n\n`)
			data := fmt.Sprintf("{\"user_id\": \"%s\", \"queue_position\": %d, \"timestamp\": \"%s\"}",
				userID, position, time.Now().Format(time.RFC3339))

			fmt.Fprintf(w, "data: %s\n\n", data)
			if err := w.Flush(); err != nil {
				// Client Disconnected (closed the web page)
				break
			}

			// If it's the user's turn (position 1), notify them to proceed to checkout
			if position == 1 {
				fmt.Fprintf(w, "event: ready\ndata: {\"message\": \"It's your turn! Proceed to checkout.\"}\n\n")
				w.Flush()
				break
			}

			time.Sleep(2 * time.Second)
		}
	}))

	return nil
}
