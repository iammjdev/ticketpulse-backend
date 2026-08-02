package handler

import (
	"errors"
	"io"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/iammjdev/ticketpulse-backend/internal/repository"
	"github.com/iammjdev/ticketpulse-backend/internal/service"
	"github.com/iammjdev/ticketpulse-backend/pkg/messaging"
)

// SeatHoldSeconds mirrors the payment window the zone-based flow grants (OrderWorker's
// OrderExpirySeconds), so a specific-seat hold and its eventual order expiry stay in sync.
const SeatHoldSeconds = 600

// World-space canvas the AI-inferred layout is scaled into — mirrors the frontend's
// SEAT_MAP_WORLD_WIDTH/HEIGHT (src/types/seatMap.ts) so an AI-generated map renders where
// the grid-based generator's seats do.
const (
	aiSeatMapWorldWidth  = 1000.0
	aiSeatMapWorldHeight = 620.0
	aiSeatColSpacing     = 26.0
	maxPosterImageBytes  = 8 << 20 // 8MB
)

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

// previewRow/previewZone/previewStage mirror service.AILayout but with start_x/start_y (and
// the stage anchor) already scaled from image percentages into the 1000x620 world-space
// canvas the frontend's CanvasSeatMap renders — the review drawer's live preview and the
// eventual confirm payload both work in world-space, not percentages.
type previewRow struct {
	RowLabel  string  `json:"row_label"`
	SeatCount int     `json:"seat_count"`
	StartX    float64 `json:"start_x"`
	StartY    float64 `json:"start_y"`
}

type previewZone struct {
	ZoneName string       `json:"zone_name"`
	Price    float64      `json:"price"`
	ColorHex string       `json:"color_hex"`
	SeatType string       `json:"seat_type"`
	Rows     []previewRow `json:"rows"`
}

type previewStage struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type previewLayout struct {
	Stage previewStage  `json:"stage"`
	Zones []previewZone `json:"zones"`
}

func toPreviewLayout(layout *service.AILayout) previewLayout {
	out := previewLayout{
		Stage: previewStage{
			X: clampWorld(layout.Stage.X/100*aiSeatMapWorldWidth, aiSeatMapWorldWidth),
			Y: clampWorld(layout.Stage.Y/100*aiSeatMapWorldHeight, aiSeatMapWorldHeight),
		},
		Zones: make([]previewZone, 0, len(layout.Zones)),
	}
	for _, z := range layout.Zones {
		rows := make([]previewRow, 0, len(z.Rows))
		for _, row := range z.Rows {
			rows = append(rows, previewRow{
				RowLabel:  strings.TrimSpace(row.RowLabel),
				SeatCount: row.SeatCount,
				StartX:    clampWorld(row.StartX/100*aiSeatMapWorldWidth, aiSeatMapWorldWidth),
				StartY:    clampWorld(row.StartY/100*aiSeatMapWorldHeight, aiSeatMapWorldHeight),
			})
		}
		seatType := strings.ToUpper(strings.TrimSpace(z.SeatType))
		if seatType != "STANDING" {
			seatType = "SEATED"
		}
		out.Zones = append(out.Zones, previewZone{
			ZoneName: strings.TrimSpace(z.ZoneName),
			Price:    z.Price,
			ColorHex: z.ColorHex,
			SeatType: seatType,
			Rows:     rows,
		})
	}
	return out
}

// AdminAIPreviewSeats accepts a poster/seat-map image upload and asks the AI vision
// synthesizer to infer a stage anchor and zones/rows from it (see
// internal/service.GenerateSeatLayoutFromPoster for the vision-vs-deterministic-fallback
// split). This is preview-only — nothing is written to Postgres. The admin reviews/edits the
// returned layout in the frontend's AI Layout Review Drawer, then calls AdminAIConfirmSeats
// to persist it. ADMIN only.
func (h *SeatHandler) AdminAIPreviewSeats(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("poster_image")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "poster_image file is required"})
	}
	if fileHeader.Size > maxPosterImageBytes {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Image must be 8MB or smaller"})
	}

	contentType := fileHeader.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "poster_image must be an image file"})
	}

	file, err := fileHeader.Open()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to read uploaded image"})
	}
	defer file.Close()

	imageBytes, err := io.ReadAll(file)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to read uploaded image"})
	}

	layout, err := service.GenerateSeatLayoutFromPoster(c.Context(), imageBytes, contentType)
	if err != nil {
		log.Printf("⚠️ AI seat layout generation failed: %v\n", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to analyze poster image"})
	}
	if len(layout.Zones) == 0 {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": "Could not infer any seating zones from this image"})
	}

	return c.JSON(toPreviewLayout(layout))
}

type confirmSeatsRequest struct {
	Zones []previewZone `json:"zones"`
}

// AdminAIConfirmSeats takes the (admin-reviewed, possibly hand-edited) layout returned by
// AdminAIPreviewSeats and persists it for real: upserts the zones into seat_zones, pre-warms
// their Redis stock, and bulk-creates the seats. This is the only step in the AI seat-map flow
// that writes to Postgres. Returns the event's full current seat map. ADMIN only.
func (h *SeatHandler) AdminAIConfirmSeats(c *fiber.Ctx) error {
	eventID := c.Params("id")

	var req confirmSeatsRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	zoneInputs := make([]repository.NewZone, 0, len(req.Zones))
	for _, z := range req.Zones {
		capacity := 0
		for _, r := range z.Rows {
			capacity += r.SeatCount
		}
		name := strings.TrimSpace(z.ZoneName)
		seatType := strings.ToUpper(strings.TrimSpace(z.SeatType))
		if seatType != "STANDING" {
			seatType = "SEATED"
		}
		if capacity <= 0 || name == "" {
			continue
		}
		zoneInputs = append(zoneInputs, repository.NewZone{
			Name:          name,
			Type:          seatType,
			Price:         z.Price,
			TotalCapacity: capacity,
		})
	}
	if len(zoneInputs) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "At least one zone with seats is required"})
	}

	zones, err := h.events.UpdateEventZones(c.Context(), eventID, zoneInputs)
	if err != nil {
		if errors.Is(err, repository.ErrEventNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Event not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save zones"})
	}
	for _, z := range zones {
		if err := h.redis.WarmupStock(c.Context(), eventID, z.ID, z.TotalCapacity); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Zones saved, but failed to pre-warm Redis stock"})
		}
	}
	zoneIDByName := make(map[string]string, len(zones))
	for _, z := range zones {
		zoneIDByName[z.Name] = z.ID
	}

	seatInputs := make([]repository.NewSeat, 0)
	for _, z := range req.Zones {
		zoneID, ok := zoneIDByName[strings.TrimSpace(z.ZoneName)]
		if !ok {
			continue
		}
		for _, row := range z.Rows {
			rowLabel := strings.TrimSpace(row.RowLabel)
			if rowLabel == "" || row.SeatCount <= 0 {
				continue
			}
			baseX := clampWorld(row.StartX, aiSeatMapWorldWidth)
			baseY := clampWorld(row.StartY, aiSeatMapWorldHeight)
			for n := 1; n <= row.SeatCount; n++ {
				seatInputs = append(seatInputs, repository.NewSeat{
					ZoneID:     zoneID,
					RowLabel:   rowLabel,
					SeatNumber: n,
					PositionX:  clampWorld(baseX+float64(n-1)*aiSeatColSpacing, aiSeatMapWorldWidth),
					PositionY:  baseY,
				})
			}
		}
	}
	if len(seatInputs) == 0 {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": "Zones had no seats"})
	}

	if err := h.seats.BulkCreateSeats(c.Context(), eventID, seatInputs); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save seats"})
	}

	seats, err := h.seats.GetSeatsByEventID(c.Context(), eventID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Seats saved, but failed to reload seat map"})
	}
	for i := range seats {
		seats[i].Status = "AVAILABLE"
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"seats": seats, "zones": zones})
}

func clampWorld(v, max float64) float64 {
	if v < 0 {
		return 0
	}
	if v > max-10 {
		return max - 10
	}
	return v
}
