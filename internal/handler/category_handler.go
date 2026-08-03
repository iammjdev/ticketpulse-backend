package handler

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
)

type CategoryHandler struct {
	categories repository.CategoryRepository
}

func NewCategoryHandler(categories repository.CategoryRepository) *CategoryHandler {
	return &CategoryHandler{categories: categories}
}

func categoryResponse(cat *domain.Category) fiber.Map {
	return fiber.Map{
		"id":         cat.ID,
		"name":       cat.Name,
		"slug":       cat.Slug,
		"created_at": cat.CreatedAt,
	}
}

func categoryListResponse(categories []*domain.Category, total, page, limit int) fiber.Map {
	items := make([]fiber.Map, 0, len(categories))
	for _, cat := range categories {
		items = append(items, categoryResponse(cat))
	}
	totalPages := 0
	if limit > 0 {
		totalPages = (total + limit - 1) / limit
	}
	return fiber.Map{
		"categories":  items,
		"total":       total,
		"page":        page,
		"limit":       limit,
		"total_pages": totalPages,
	}
}

func parseCategoryListFilter(c *fiber.Ctx) repository.CategoryListFilter {
	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("limit", "20"))
	return repository.CategoryListFilter{
		Search: strings.TrimSpace(c.Query("search")),
		Page:   page,
		Limit:  limit,
	}
}

func normalizeCategoryPagination(filter repository.CategoryListFilter) (page, limit int) {
	page = max(filter.Page, 1)
	limit = filter.Limit
	if limit < 1 || limit > 100 {
		limit = 20
	}
	return page, limit
}

// AdminListCategories returns categories with optional search, paginated, for the async
// combobox's option list. ADMIN only.
func (h *CategoryHandler) AdminListCategories(c *fiber.Ctx) error {
	filter := parseCategoryListFilter(c)
	categories, total, err := h.categories.ListCategories(c.Context(), filter)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch categories"})
	}
	page, limit := normalizeCategoryPagination(filter)
	return c.JSON(categoryListResponse(categories, total, page, limit))
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case !lastDash && b.Len() > 0:
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func (h *CategoryHandler) generateUniqueSlug(ctx context.Context, name string) (string, error) {
	base := slugify(name)
	if base == "" {
		base = "category"
	}
	slug := base
	for i := 2; ; i++ {
		taken, err := h.categories.SlugExists(ctx, slug)
		if err != nil {
			return "", err
		}
		if !taken {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
}

type categoryRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// CreateCategory creates a new category. When slug is omitted, it is auto-generated from the
// name and disambiguated with a numeric suffix on collision. ADMIN only.
func (h *CategoryHandler) CreateCategory(c *fiber.Ctx) error {
	var req categoryRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "name is required"})
	}

	slug := strings.Trim(strings.ToLower(strings.TrimSpace(req.Slug)), "-")
	if slug == "" {
		generated, err := h.generateUniqueSlug(c.Context(), req.Name)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to generate slug"})
		}
		slug = generated
	} else if taken, err := h.categories.SlugExists(c.Context(), slug); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to validate slug"})
	} else if taken {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "Slug already in use"})
	}

	category, err := h.categories.Create(c.Context(), repository.NewCategory{Name: req.Name, Slug: slug})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create category"})
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"category": categoryResponse(category)})
}

// UpdateCategory patches an existing category's name/slug. ADMIN only.
func (h *CategoryHandler) UpdateCategory(c *fiber.Ctx) error {
	id := c.Params("id")
	existing, err := h.categories.FindByID(c.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrCategoryNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Category not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch category"})
	}

	var req categoryRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	patch := repository.CategoryUpdate{}
	if name := strings.TrimSpace(req.Name); name != "" {
		patch.Name = &name
	}
	if slug := strings.Trim(strings.ToLower(strings.TrimSpace(req.Slug)), "-"); slug != "" && slug != existing.Slug {
		if taken, err := h.categories.SlugExists(c.Context(), slug); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to validate slug"})
		} else if taken {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "Slug already in use"})
		}
		patch.Slug = &slug
	}

	updated, err := h.categories.Update(c.Context(), id, patch)
	if err != nil {
		if errors.Is(err, repository.ErrCategoryNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Category not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update category"})
	}
	return c.JSON(fiber.Map{"category": categoryResponse(updated)})
}

// DeleteCategory removes a category. Any events referencing it fall back to no category
// (ON DELETE SET NULL on events.category_id) rather than blocking the delete. ADMIN only.
func (h *CategoryHandler) DeleteCategory(c *fiber.Ctx) error {
	id := c.Params("id")
	if err := h.categories.Delete(c.Context(), id); err != nil {
		if errors.Is(err, repository.ErrCategoryNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Category not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to delete category"})
	}
	return c.JSON(fiber.Map{"message": "Category deleted", "id": id})
}
