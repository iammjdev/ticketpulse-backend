package handler

import (
	"errors"
	"strconv"
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

func adminEventSummaryResponse(e *domain.AdminEventSummary) fiber.Map {
	return fiber.Map{
		"id":             e.ID,
		"title":          e.Title,
		"banner_url":     e.BannerURL,
		"event_date":     e.EventDate,
		"status":         e.Status,
		"venue_name":     e.VenueName,
		"venue_location": e.VenueLocation,
		"total_capacity": e.TotalCapacity,
		"tickets_sold":   e.TicketsSold,
		"revenue":        e.Revenue,
	}
}

func adminEventListResponse(events []*domain.AdminEventSummary, total, page, limit int) fiber.Map {
	items := make([]fiber.Map, 0, len(events))
	for _, e := range events {
		items = append(items, adminEventSummaryResponse(e))
	}
	totalPages := 0
	if limit > 0 {
		totalPages = (total + limit - 1) / limit
	}
	return fiber.Map{
		"events":      items,
		"total":       total,
		"page":        page,
		"limit":       limit,
		"total_pages": totalPages,
	}
}

// AdminListEvents returns every event regardless of status, with sold-ticket-count and gross
// revenue aggregated from paid orders, optionally filtered by status and paginated. ADMIN only.
func (h *EventHandler) AdminListEvents(c *fiber.Ctx) error {
	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("limit", "20"))
	filter := repository.AdminEventListFilter{
		Status: strings.ToUpper(strings.TrimSpace(c.Query("status"))),
		Page:   page,
		Limit:  limit,
	}

	events, total, err := h.events.ListAdminEvents(c.Context(), filter)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch events"})
	}

	appliedPage := max(page, 1)
	appliedLimit := limit
	if appliedLimit < 1 || appliedLimit > 100 {
		appliedLimit = 20
	}
	return c.JSON(adminEventListResponse(events, total, appliedPage, appliedLimit))
}

type updateZonesRequest struct {
	Zones []createZoneRequest `json:"zones"`
}

// UpdateZones upserts an event's ticket zones (matched by name) and pre-warms each zone's
// Redis stock counter to the new total_capacity, mirroring CreateEvent's warmup step. Existing
// zones not in the request body are left untouched — zones are never deleted here since
// tickets FK to seat_zones. ADMIN only.
func (h *EventHandler) UpdateZones(c *fiber.Ctx) error {
	eventID := c.Params("id")

	var req updateZonesRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}
	if len(req.Zones) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "At least one zone is required"})
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

	updated, err := h.events.UpdateEventZones(c.Context(), eventID, zones)
	if err != nil {
		if errors.Is(err, repository.ErrEventNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Event not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update zones"})
	}

	for _, z := range updated {
		if err := h.redis.WarmupStock(c.Context(), eventID, z.ID, z.TotalCapacity); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error":   "Zones updated, but failed to pre-warm Redis stock",
				"zones":   updated,
				"details": err.Error(),
			})
		}
	}

	zonesResponse := make([]fiber.Map, 0, len(updated))
	for _, z := range updated {
		zonesResponse = append(zonesResponse, zoneResponse(z))
	}
	return c.JSON(fiber.Map{"zones": zonesResponse})
}

type updateEventStatusRequest struct {
	Status string `json:"status"`
}

// UpdateEventStatus transitions an event through its lifecycle (PRE_WAITING/LIVE/ENDED, or
// back to UPCOMING) in PostgreSQL and mirrors the value into the Redis event:{id}:status key
// that queue/reservation code paths read for fast, DB-free status checks. ADMIN only.
func (h *EventHandler) UpdateEventStatus(c *fiber.Ctx) error {
	eventID := c.Params("id")

	var req updateEventStatusRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	status := domain.EventStatus(strings.ToUpper(strings.TrimSpace(req.Status)))
	switch status {
	case domain.EventUpcoming, domain.EventPreWaiting, domain.EventLive, domain.EventEnded:
		// valid
	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "status must be one of UPCOMING, PRE_WAITING, LIVE, ENDED"})
	}

	event, err := h.events.UpdateEventStatus(c.Context(), eventID, status)
	if err != nil {
		if errors.Is(err, repository.ErrEventNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Event not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update event status"})
	}

	if err := h.redis.SetEventStatus(c.Context(), eventID, string(status)); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   "Event status updated in database, but failed to sync Redis",
			"event":   eventDetailResponse(event),
			"details": err.Error(),
		})
	}

	return c.JSON(fiber.Map{"event": eventDetailResponse(event)})
}
