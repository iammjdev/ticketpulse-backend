package handler

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
	"github.com/iammjdev/ticketpulse-backend/internal/service"
)

type NewsHandler struct {
	news *service.NewsService
}

func NewNewsHandler(news *service.NewsService) *NewsHandler {
	return &NewsHandler{news: news}
}

func newsArticleResponse(a *domain.NewsArticle) fiber.Map {
	return fiber.Map{
		"id":           a.ID,
		"title":        a.Title,
		"slug":         a.Slug,
		"summary":      a.Summary,
		"content":      a.Content,
		"cover_image":  a.CoverImage,
		"category":     a.Category,
		"is_published": a.IsPublished,
		"views_count":  a.ViewsCount,
		"published_at": a.PublishedAt,
		"created_at":   a.CreatedAt,
		"updated_at":   a.UpdatedAt,
	}
}

func newsListResponse(articles []*domain.NewsArticle, total, page, limit int) fiber.Map {
	items := make([]fiber.Map, 0, len(articles))
	for _, a := range articles {
		items = append(items, newsArticleResponse(a))
	}
	totalPages := 0
	if limit > 0 {
		totalPages = (total + limit - 1) / limit
	}
	return fiber.Map{
		"articles":    items,
		"total":       total,
		"page":        page,
		"limit":       limit,
		"total_pages": totalPages,
	}
}

func parseListFilter(c *fiber.Ctx, publishedOnly bool) repository.NewsListFilter {
	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("limit", "12"))
	return repository.NewsListFilter{
		Category:      strings.ToUpper(strings.TrimSpace(c.Query("category"))),
		Search:        strings.TrimSpace(c.Query("search")),
		Page:          page,
		Limit:         limit,
		PublishedOnly: publishedOnly,
	}
}

// normalizePagination mirrors the repository's own page/limit clamping so the response
// envelope reports the values that were actually applied to the query.
func normalizePagination(filter repository.NewsListFilter) (page, limit int) {
	page = max(filter.Page, 1)
	limit = filter.Limit
	if limit < 1 {
		limit = 12
	}
	return page, limit
}

// ListNews returns published articles with optional category/search filters and pagination.
// Read-through cached for the default (page 1, unfiltered) query. Public.
func (h *NewsHandler) ListNews(c *fiber.Ctx) error {
	filter := parseListFilter(c, true)
	articles, total, err := h.news.List(c.Context(), filter)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch news"})
	}
	page, limit := normalizePagination(filter)
	return c.JSON(newsListResponse(articles, total, page, limit))
}

// GetNewsBySlug returns a single published article and bumps its view count. Public.
func (h *NewsHandler) GetNewsBySlug(c *fiber.Ctx) error {
	slug := c.Params("slug")
	article, err := h.news.GetBySlug(c.Context(), slug, true)
	if err != nil {
		if errors.Is(err, repository.ErrNewsNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Article not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch article"})
	}
	return c.JSON(fiber.Map{"article": newsArticleResponse(article)})
}

// AdminListNews returns every article regardless of publish state, uncached, for the CMS
// data table. ADMIN only.
func (h *NewsHandler) AdminListNews(c *fiber.Ctx) error {
	filter := parseListFilter(c, false)
	articles, total, err := h.news.List(c.Context(), filter)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch news"})
	}
	page, limit := normalizePagination(filter)
	return c.JSON(newsListResponse(articles, total, page, limit))
}

type newsArticleRequest struct {
	Title       string `json:"title"`
	Slug        string `json:"slug"`
	Summary     string `json:"summary"`
	Content     string `json:"content"`
	CoverImage  string `json:"cover_image"`
	Category    string `json:"category"`
	IsPublished *bool  `json:"is_published"`
}

func resolveCategory(raw string) domain.NewsCategory {
	category := domain.NewsCategory(strings.ToUpper(strings.TrimSpace(raw)))
	if !category.Valid() {
		return domain.NewsAnnouncement
	}
	return category
}

// CreateNews creates a new article. When slug is omitted, it is auto-generated from the
// title and disambiguated with a numeric suffix on collision. ADMIN only.
func (h *NewsHandler) CreateNews(c *fiber.Ctx) error {
	var req newsArticleRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	req.Title = strings.TrimSpace(req.Title)
	req.Summary = strings.TrimSpace(req.Summary)
	req.Content = strings.TrimSpace(req.Content)
	if req.Title == "" || req.Summary == "" || req.Content == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "title, summary, and content are required"})
	}

	slug := strings.Trim(strings.ToLower(strings.TrimSpace(req.Slug)), "-")
	if slug == "" {
		generated, err := h.news.GenerateUniqueSlug(c.Context(), req.Title)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to generate slug"})
		}
		slug = generated
	} else if taken, err := h.news.SlugTaken(c.Context(), slug); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to validate slug"})
	} else if taken {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "Slug already in use"})
	}

	isPublished := true
	if req.IsPublished != nil {
		isPublished = *req.IsPublished
	}

	article, err := h.news.Create(c.Context(), repository.NewNewsArticle{
		Title:       req.Title,
		Slug:        slug,
		Summary:     req.Summary,
		Content:     req.Content,
		CoverImage:  req.CoverImage,
		Category:    resolveCategory(req.Category),
		IsPublished: isPublished,
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create article"})
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"article": newsArticleResponse(article)})
}

// UpdateNews patches an existing article and invalidates both the list cache and the
// (old and, if changed, new) slug cache entries. ADMIN only.
func (h *NewsHandler) UpdateNews(c *fiber.Ctx) error {
	id := c.Params("id")
	existing, err := h.news.GetByID(c.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNewsNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Article not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch article"})
	}

	var req newsArticleRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	patch := repository.NewsUpdate{}
	if title := strings.TrimSpace(req.Title); title != "" {
		patch.Title = &title
	}
	if summary := strings.TrimSpace(req.Summary); summary != "" {
		patch.Summary = &summary
	}
	if content := strings.TrimSpace(req.Content); content != "" {
		patch.Content = &content
	}
	if req.CoverImage != "" {
		patch.CoverImage = &req.CoverImage
	}
	if req.Category != "" {
		category := resolveCategory(req.Category)
		patch.Category = &category
	}
	if req.IsPublished != nil {
		patch.IsPublished = req.IsPublished
	}
	if slug := strings.Trim(strings.ToLower(strings.TrimSpace(req.Slug)), "-"); slug != "" && slug != existing.Slug {
		if taken, err := h.news.SlugTaken(c.Context(), slug); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to validate slug"})
		} else if taken {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "Slug already in use"})
		}
		patch.Slug = &slug
	}

	updated, err := h.news.Update(c.Context(), id, existing.Slug, patch)
	if err != nil {
		if errors.Is(err, repository.ErrNewsNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Article not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update article"})
	}
	return c.JSON(fiber.Map{"article": newsArticleResponse(updated)})
}

// DeleteNews removes an article and invalidates its cache entries. ADMIN only.
func (h *NewsHandler) DeleteNews(c *fiber.Ctx) error {
	id := c.Params("id")
	existing, err := h.news.GetByID(c.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNewsNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Article not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch article"})
	}

	if err := h.news.Delete(c.Context(), id, existing.Slug); err != nil {
		if errors.Is(err, repository.ErrNewsNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Article not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to delete article"})
	}
	return c.JSON(fiber.Map{"message": "Article deleted"})
}
