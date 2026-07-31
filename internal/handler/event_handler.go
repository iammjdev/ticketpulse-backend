package handler

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
)

type EventHandler struct {
	events repository.EventRepository
	redis  repository.RedisRepository
}

func NewEventHandler(events repository.EventRepository, redis repository.RedisRepository) *EventHandler {
	return &EventHandler{events: events, redis: redis}
}

func eventSummaryResponse(e *domain.EventSummary) fiber.Map {
	return fiber.Map{
		"id":             e.ID,
		"title":          e.Title,
		"banner_url":     e.BannerURL,
		"event_date":     e.EventDate,
		"status":         e.Status,
		"venue_name":     e.VenueName,
		"venue_location": e.VenueLocation,
		"min_price":      e.MinPrice,
	}
}

func zoneResponse(z domain.Zone) fiber.Map {
	return fiber.Map{
		"id":              z.ID,
		"name":            z.Name,
		"type":            z.Type,
		"price":           z.Price,
		"total_capacity":  z.TotalCapacity,
		"available_seats": z.AvailableSeats,
	}
}

func eventDetailResponse(d *domain.EventDetail) fiber.Map {
	zones := make([]fiber.Map, 0, len(d.Zones))
	for _, z := range d.Zones {
		zones = append(zones, zoneResponse(z))
	}
	return fiber.Map{
		"id":          d.ID,
		"title":       d.Title,
		"description": d.Description,
		"banner_url":  d.BannerURL,
		"event_date":  d.EventDate,
		"status":      d.Status,
		"venue": fiber.Map{
			"id":       d.Venue.ID,
			"name":     d.Venue.Name,
			"location": d.Venue.Location,
			"capacity": d.Venue.Capacity,
		},
		"zones": zones,
	}
}

// ListEvents returns all non-ENDED events with their venue and starting price, ordered by
// upcoming date. Public.
func (h *EventHandler) ListEvents(c *fiber.Ctx) error {
	events, err := h.events.ListActiveEvents(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch events"})
	}

	response := make([]fiber.Map, 0, len(events))
	for _, e := range events {
		response = append(response, eventSummaryResponse(e))
	}
	return c.JSON(fiber.Map{"events": response})
}

// GetEvent returns a single event joined with its venue and ticket zones. Public.
func (h *EventHandler) GetEvent(c *fiber.Ctx) error {
	id := c.Params("id")
	event, err := h.events.FindEventByID(c.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrEventNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Event not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch event"})
	}
	return c.JSON(fiber.Map{"event": eventDetailResponse(event)})
}

type createZoneRequest struct {
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	Price         float64 `json:"price"`
	TotalCapacity int     `json:"total_capacity"`
}

type createEventRequest struct {
	VenueID     string              `json:"venue_id"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	BannerURL   string              `json:"banner_url"`
	EventDate   string              `json:"event_date"`
	Status      string              `json:"status"`
	Zones       []createZoneRequest `json:"zones"`
}

// CreateEvent creates a new event with its ticket zones and immediately pre-warms each
// zone's stock counter in Redis so reservations work without a manual /tickets/warmup call.
// ADMIN only.
func (h *EventHandler) CreateEvent(c *fiber.Ctx) error {
	var req createEventRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	req.Title = strings.TrimSpace(req.Title)
	if req.VenueID == "" || req.Title == "" || req.EventDate == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "venue_id, title, and event_date are required"})
	}
	if len(req.Zones) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "At least one zone is required"})
	}

	status := domain.EventStatus(strings.ToUpper(strings.TrimSpace(req.Status)))
	switch status {
	case domain.EventUpcoming, domain.EventPreWaiting, domain.EventLive, domain.EventEnded:
		// valid
	default:
		status = domain.EventUpcoming
	}

	zones := make([]repository.NewZone, 0, len(req.Zones))
	for _, z := range req.Zones {
		zoneType := strings.ToUpper(strings.TrimSpace(z.Type))
		if zoneType != "STANDING" {
			zoneType = "SEATED"
		}
		if strings.TrimSpace(z.Name) == "" || z.Price < 0 || z.TotalCapacity <= 0 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Each zone needs a name, a non-negative price, and a positive total_capacity"})
		}
		zones = append(zones, repository.NewZone{
			Name:          strings.TrimSpace(z.Name),
			Type:          zoneType,
			Price:         z.Price,
			TotalCapacity: z.TotalCapacity,
		})
	}

	event, err := h.events.CreateEventWithZones(c.Context(), req.VenueID, req.Title, req.Description, req.BannerURL, req.EventDate, status, zones)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create event"})
	}

	for _, z := range event.Zones {
		if err := h.redis.WarmupStock(c.Context(), event.ID, z.ID, z.TotalCapacity); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error":   "Event created, but failed to pre-warm Redis stock",
				"event":   eventDetailResponse(event),
				"details": err.Error(),
			})
		}
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"event": eventDetailResponse(event)})
}
