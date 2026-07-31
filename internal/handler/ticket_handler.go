package handler

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/iammjdev/ticketpulse-backend/internal/repository"
	"github.com/iammjdev/ticketpulse-backend/pkg/messaging"
)

const defaultReserveZoneID = "22222222-2222-2222-2222-222222222222"
const defaultReservePrice = 1500.00

type TicketHandler struct {
	redisRepo     repository.RedisRepository
	redisClient   *redis.Client
	kafkaProducer *messaging.KafkaProducer
	events        repository.EventRepository
	users         repository.UserRepository
}

func NewTicketHandler(
	redisRepo repository.RedisRepository,
	redisClient *redis.Client,
	kafkaProducer *messaging.KafkaProducer,
	events repository.EventRepository,
	users repository.UserRepository,
) *TicketHandler {
	return &TicketHandler{
		redisRepo:     redisRepo,
		redisClient:   redisClient,
		kafkaProducer: kafkaProducer,
		events:        events,
		users:         users,
	}
}

type reserveRequest struct {
	EventID  string  `json:"event_id"`
	ZoneID   string  `json:"zone_id"`
	UserID   string  `json:"user_id"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

// Reserve performs the atomic Redis stock deduction for /api/v1/tickets/reserve. Events
// flagged requires_id_verification block reservation until the user has a national ID /
// passport on file — the frontend surfaces this as a progressive verification modal.
func (h *TicketHandler) Reserve(c *fiber.Ctx) error {
	var req reserveRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	// Authenticated user id from JWT always takes precedence over the request body
	if userID, ok := c.Locals("userId").(string); ok && userID != "" {
		req.UserID = userID
	}

	// Provide default fallback values for testing payload
	if req.ZoneID == "" {
		req.ZoneID = defaultReserveZoneID
	}
	if req.Price == 0 {
		req.Price = defaultReservePrice
	}

	needsVerification, err := h.events.RequiresIDVerification(c.Context(), req.EventID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to check event requirements"})
	}
	if needsVerification {
		user, err := h.users.FindByID(c.Context(), req.UserID)
		if err != nil && !errors.Is(err, repository.ErrUserNotFound) {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to verify identity"})
		}
		if err != nil || user.NationalID == "" {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error":   "IDENTITY_VERIFICATION_REQUIRED",
				"message": "Thai National ID / Passport number is required for this event.",
			})
		}
	}

	// Atomic Inventory Deduction in Redis Lua
	status, err := h.redisRepo.ReserveTicket(c.Context(), req.EventID, req.ZoneID, req.Quantity)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if status != "RESERVED" {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"status": status, "message": "Sorry, tickets for this zone are sold out!"})
	}

	// Dequeue user from Redis Virtual Waiting Room after successful reservation
	if err := h.redisRepo.DequeueUser(c.Context(), req.EventID, req.UserID); err != nil {
		log.Printf("⚠️ Failed to dequeue user %s from Redis: %v\n", req.UserID, err)
	} else {
		log.Printf("🗑️ Successfully dequeued user %s from Redis queue\n", req.UserID)
	}

	// Publish Event to Kafka for Asynchronous Processing
	orderID := uuid.New().String()
	event := messaging.OrderCreatedEvent{
		OrderID:   orderID,
		EventID:   req.EventID,
		ZoneID:    req.ZoneID,
		UserID:    req.UserID,
		Quantity:  req.Quantity,
		Price:     req.Price,
		Timestamp: time.Now(),
	}

	if err := h.kafkaProducer.PublishOrderCreated(c.Context(), event); err != nil {
		log.Printf("⚠️ Failed to publish order event to Kafka: %v\n", err)
	} else {
		log.Printf("📢 Published OrderCreatedEvent to Kafka: OrderID=%s\n", orderID)
	}

	holdKey := fmt.Sprintf("hold:%s:%s:%d:%s", req.EventID, req.ZoneID, req.Quantity, req.UserID)
	_ = h.redisClient.Set(c.Context(), holdKey, "HELD", 10*time.Minute).Err()

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"status":   "RESERVED",
		"message":  "Ticket reserved! Order is being processed asynchronously.",
		"order_id": orderID,
	})
}
