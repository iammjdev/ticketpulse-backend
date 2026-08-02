package handler

import (
	"errors"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/iammjdev/ticketpulse-backend/internal/repository"
	"github.com/iammjdev/ticketpulse-backend/pkg/messaging"
)

// SeatHoldSeconds mirrors the payment window the zone-based flow grants (OrderWorker's
// OrderExpirySeconds), so a specific-seat hold and its eventual order expiry stay in sync.
const SeatHoldSeconds = 600

type SeatHandler struct {
	seats         repository.SeatRepository
	redis         repository.RedisRepository
	kafkaProducer *messaging.KafkaProducer
	events        repository.EventRepository
	users         repository.UserRepository
}

func NewSeatHandler(
	seats repository.SeatRepository,
	redis repository.RedisRepository,
	kafkaProducer *messaging.KafkaProducer,
	events repository.EventRepository,
	users repository.UserRepository,
) *SeatHandler {
	return &SeatHandler{seats: seats, redis: redis, kafkaProducer: kafkaProducer, events: events, users: users}
}

// GetEventSeats returns every seat in the event's map merged with live HELD/SOLD status from
// Redis. Seats with no Redis entry are AVAILABLE. Public.
func (h *SeatHandler) GetEventSeats(c *fiber.Ctx) error {
	eventID := c.Params("id")

	seats, err := h.seats.GetSeatsByEventID(c.Context(), eventID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch seats"})
	}

	statuses, err := h.redis.GetSeatStatuses(c.Context(), eventID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch seat status"})
	}

	for i := range seats {
		if status, ok := statuses[seats[i].ID]; ok {
			seats[i].Status = status
		} else {
			seats[i].Status = "AVAILABLE"
		}
	}

	return c.JSON(fiber.Map{"seats": seats})
}

type reserveSeatRequest struct {
	EventID string `json:"event_id"`
	SeatID  string `json:"seat_id"`
}

// ReserveSeat atomically holds one specific seat via the reserve_specific_seat Lua script,
// then publishes an OrderCreatedEvent so the existing OrderWorker persists the PENDING order
// exactly like the zone-based /tickets/reserve flow — the seat map only changes how the seat
// is picked, not how the resulting order is processed. Requires JWT auth.
func (h *SeatHandler) ReserveSeat(c *fiber.Ctx) error {
	var req reserveSeatRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}
	req.EventID = strings.TrimSpace(req.EventID)
	req.SeatID = strings.TrimSpace(req.SeatID)
	if req.EventID == "" || req.SeatID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "event_id and seat_id are required"})
	}

	userID, _ := c.Locals("userId").(string)
	if userID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Missing authenticated user"})
	}

	needsVerification, err := h.events.RequiresIDVerification(c.Context(), req.EventID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to check event requirements"})
	}
	if needsVerification {
		user, err := h.users.FindByID(c.Context(), userID)
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

	seat, price, err := h.seats.FindSeatForReservation(c.Context(), req.EventID, req.SeatID)
	if err != nil {
		if errors.Is(err, repository.ErrSeatNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Seat not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to look up seat"})
	}

	held, err := h.redis.ReserveSpecificSeat(c.Context(), req.EventID, req.SeatID, userID, SeatHoldSeconds)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if !held {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"status": "TAKEN", "message": "Sorry, this seat is no longer available."})
	}

	orderID := uuid.New().String()
	orderEvent := messaging.OrderCreatedEvent{
		OrderID:   orderID,
		EventID:   req.EventID,
		ZoneID:    seat.ZoneID,
		UserID:    userID,
		Quantity:  1,
		Price:     price,
		Timestamp: time.Now(),
	}
	if err := h.kafkaProducer.PublishOrderCreated(c.Context(), orderEvent); err != nil {
		log.Printf("⚠️ Failed to publish seat order event to Kafka: %v\n", err)
	}

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"status":   "HELD",
		"message":  "Seat held! Order is being processed asynchronously.",
		"order_id": orderID,
		"seat_id":  req.SeatID,
	})
}

type adminSeatRequest struct {
	ZoneID     string  `json:"zone_id"`
	RowLabel   string  `json:"row_label"`
	SeatNumber int     `json:"seat_number"`
	PositionX  float64 `json:"position_x"`
	PositionY  float64 `json:"position_y"`
}

type adminBulkCreateSeatsRequest struct {
	Seats []adminSeatRequest `json:"seats"`
}

// AdminBulkCreateSeats batch-inserts (or upserts) the seat map layout coordinates for an
// event. ADMIN only.
func (h *SeatHandler) AdminBulkCreateSeats(c *fiber.Ctx) error {
	eventID := c.Params("id")

	var req adminBulkCreateSeatsRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}
	if len(req.Seats) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "At least one seat is required"})
	}

	seats := make([]repository.NewSeat, 0, len(req.Seats))
	for _, s := range req.Seats {
		rowLabel := strings.TrimSpace(s.RowLabel)
		if s.ZoneID == "" || rowLabel == "" || s.SeatNumber <= 0 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Each seat needs a zone_id, row_label, and positive seat_number"})
		}
		seats = append(seats, repository.NewSeat{
			ZoneID:     s.ZoneID,
			RowLabel:   rowLabel,
			SeatNumber: s.SeatNumber,
			PositionX:  s.PositionX,
			PositionY:  s.PositionY,
		})
	}

	if err := h.seats.BulkCreateSeats(c.Context(), eventID, seats); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create seats"})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"message": "Seats created", "count": len(seats)})
}
