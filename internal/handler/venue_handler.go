package handler

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
)

type VenueHandler struct {
	venues repository.VenueRepository
}

func NewVenueHandler(venues repository.VenueRepository) *VenueHandler {
	return &VenueHandler{venues: venues}
}

func venueResponse(v *domain.Venue) fiber.Map {
	return fiber.Map{
		"id":       v.ID,
		"name":     v.Name,
		"location": v.Location,
		"capacity": v.Capacity,
		"city":     v.City,
		"map_url":  v.MapURL,
	}
}

func venueListResponse(venues []*domain.Venue, total, page, limit int) fiber.Map {
	items := make([]fiber.Map, 0, len(venues))
	for _, v := range venues {
		items = append(items, venueResponse(v))
	}
	totalPages := 0
	if limit > 0 {
		totalPages = (total + limit - 1) / limit
	}
	return fiber.Map{
		"venues":      items,
		"total":       total,
		"page":        page,
		"limit":       limit,
		"total_pages": totalPages,
	}
}

func parseVenueListFilter(c *fiber.Ctx) repository.VenueListFilter {
	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("limit", "20"))
	return repository.VenueListFilter{
		Search: strings.TrimSpace(c.Query("search")),
		Page:   page,
		Limit:  limit,
	}
}

func normalizeVenuePagination(filter repository.VenueListFilter) (page, limit int) {
	page = max(filter.Page, 1)
	limit = filter.Limit
	if limit < 1 || limit > 100 {
		limit = 20
	}
	return page, limit
}

// AdminListVenues returns venues with optional search, paginated, for the async combobox's
// option list and the venue management table. ADMIN only.
func (h *VenueHandler) AdminListVenues(c *fiber.Ctx) error {
	filter := parseVenueListFilter(c)
	venues, total, err := h.venues.ListVenues(c.Context(), filter)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch venues"})
	}
	page, limit := normalizeVenuePagination(filter)
	return c.JSON(venueListResponse(venues, total, page, limit))
}

type venueRequest struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	City     string `json:"city"`
	Capacity int    `json:"capacity"`
	MapURL   string `json:"map_url"`
}

// CreateVenue creates a new venue. ADMIN only.
func (h *VenueHandler) CreateVenue(c *fiber.Ctx) error {
	var req venueRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Address = strings.TrimSpace(req.Address)
	if req.Name == "" || req.Address == "" || req.Capacity <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "name, address, and a positive capacity are required"})
	}

	venue, err := h.venues.Create(c.Context(), repository.NewVenue{
		Name:     req.Name,
		Address:  req.Address,
		City:     req.City,
		Capacity: req.Capacity,
		MapURL:   req.MapURL,
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create venue"})
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"venue": venueResponse(venue)})
}

// UpdateVenue patches an existing venue. ADMIN only.
func (h *VenueHandler) UpdateVenue(c *fiber.Ctx) error {
	id := c.Params("id")
	if _, err := h.venues.FindByID(c.Context(), id); err != nil {
		if errors.Is(err, repository.ErrVenueNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Venue not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch venue"})
	}

	var req venueRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	patch := repository.VenueUpdate{}
	if name := strings.TrimSpace(req.Name); name != "" {
		patch.Name = &name
	}
	if address := strings.TrimSpace(req.Address); address != "" {
		patch.Address = &address
	}
	if req.City != "" {
		patch.City = &req.City
	}
	if req.Capacity > 0 {
		patch.Capacity = &req.Capacity
	}
	if req.MapURL != "" {
		patch.MapURL = &req.MapURL
	}

	updated, err := h.venues.Update(c.Context(), id, patch)
	if err != nil {
		if errors.Is(err, repository.ErrVenueNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Venue not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update venue"})
	}
	return c.JSON(fiber.Map{"venue": venueResponse(updated)})
}

// DeleteVenue removes a venue after verifying it has no events. ADMIN only.
func (h *VenueHandler) DeleteVenue(c *fiber.Ctx) error {
	id := c.Params("id")
	if err := h.venues.Delete(c.Context(), id); err != nil {
		switch {
		case errors.Is(err, repository.ErrVenueNotFound):
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Venue not found"})
		case errors.Is(err, repository.ErrVenueHasEvents):
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "This venue has existing events and cannot be deleted"})
		default:
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to delete venue"})
		}
	}
	return c.JSON(fiber.Map{"message": "Venue deleted", "id": id})
}
