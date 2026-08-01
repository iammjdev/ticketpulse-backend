package repository

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
)

var ErrNewsNotFound = errors.New("news article not found")

// NewsListFilter narrows GET /news queries. PublishedOnly is forced true on the public
// endpoint and false on the admin listing so drafts stay visible to staff only.
type NewsListFilter struct {
	Category      string
	Search        string
	Page          int
	Limit         int
	PublishedOnly bool
}

type NewNewsArticle struct {
	Title       string
	Slug        string
	Summary     string
	Content     string
	CoverImage  string
	Category    domain.NewsCategory
	IsPublished bool
}

type NewsUpdate struct {
	Title       *string
	Slug        *string
	Summary     *string
	Content     *string
	CoverImage  *string
	Category    *domain.NewsCategory
	IsPublished *bool
}

type NewsRepository interface {
	ListNews(ctx context.Context, filter NewsListFilter) ([]*domain.NewsArticle, int, error)
	FindBySlug(ctx context.Context, slug string, publishedOnly bool) (*domain.NewsArticle, error)
	FindByID(ctx context.Context, id string) (*domain.NewsArticle, error)
	IncrementViews(ctx context.Context, id string) error
	SlugExists(ctx context.Context, slug string) (bool, error)
	Create(ctx context.Context, article NewNewsArticle) (*domain.NewsArticle, error)
	Update(ctx context.Context, id string, patch NewsUpdate) (*domain.NewsArticle, error)
	Delete(ctx context.Context, id string) error
}

type newsRepository struct {
	db *pgxpool.Pool
}

func NewNewsRepository(db *pgxpool.Pool) NewsRepository {
	return &newsRepository{db: db}
}

const newsColumns = `id, title, slug, summary, content, cover_image, category, is_published, views_count, published_at, created_at, updated_at`

func scanNewsArticle(row pgx.Row) (*domain.NewsArticle, error) {
	var a domain.NewsArticle
	var coverImage *string
	if err := row.Scan(
		&a.ID, &a.Title, &a.Slug, &a.Summary, &a.Content, &coverImage, &a.Category,
		&a.IsPublished, &a.ViewsCount, &a.PublishedAt, &a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if coverImage != nil {
		a.CoverImage = *coverImage
	}
	return &a, nil
}

func (r *newsRepository) ListNews(ctx context.Context, filter NewsListFilter) ([]*domain.NewsArticle, int, error) {
	page := max(filter.Page, 1)
	limit := filter.Limit
	if limit < 1 || limit > 50 {
		limit = 12
	}
	offset := (page - 1) * limit

	where := "WHERE 1 = 1"
	args := []any{}
	argN := 0

	nextArg := func(v any) string {
		argN++
		args = append(args, v)
		return "$" + strconv.Itoa(argN)
	}

	if filter.PublishedOnly {
		where += " AND is_published = TRUE"
	}
	if filter.Category != "" {
		where += " AND category = " + nextArg(filter.Category)
	}
	if filter.Search != "" {
		p := nextArg("%" + filter.Search + "%")
		where += " AND (title ILIKE " + p + " OR summary ILIKE " + p + ")"
	}

	var total int
	countQuery := "SELECT count(*) FROM news_articles " + where
	if err := r.db.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArg := nextArg(limit)
	offsetArg := nextArg(offset)
	query := "SELECT " + newsColumns + " FROM news_articles " + where +
		" ORDER BY published_at DESC LIMIT " + limitArg + " OFFSET " + offsetArg

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	articles := make([]*domain.NewsArticle, 0)
	for rows.Next() {
		a, err := scanNewsArticle(rows)
		if err != nil {
			return nil, 0, err
		}
		articles = append(articles, a)
	}
	return articles, total, rows.Err()
}

func (r *newsRepository) FindBySlug(ctx context.Context, slug string, publishedOnly bool) (*domain.NewsArticle, error) {
	query := "SELECT " + newsColumns + " FROM news_articles WHERE slug = $1"
	if publishedOnly {
		query += " AND is_published = TRUE"
	}
	a, err := scanNewsArticle(r.db.QueryRow(ctx, query, slug))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNewsNotFound
		}
		return nil, err
	}
	return a, nil
}

func (r *newsRepository) FindByID(ctx context.Context, id string) (*domain.NewsArticle, error) {
	query := "SELECT " + newsColumns + " FROM news_articles WHERE id = $1"
	a, err := scanNewsArticle(r.db.QueryRow(ctx, query, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNewsNotFound
		}
		return nil, err
	}
	return a, nil
}

func (r *newsRepository) IncrementViews(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `UPDATE news_articles SET views_count = views_count + 1 WHERE id = $1`, id)
	return err
}

func (r *newsRepository) SlugExists(ctx context.Context, slug string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM news_articles WHERE slug = $1)`, slug).Scan(&exists)
	return exists, err
}

func (r *newsRepository) Create(ctx context.Context, article NewNewsArticle) (*domain.NewsArticle, error) {
	query := `
		INSERT INTO news_articles (title, slug, summary, content, cover_image, category, is_published)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7)
		RETURNING id
	`
	var id string
	err := r.db.QueryRow(ctx, query,
		article.Title, article.Slug, article.Summary, article.Content, article.CoverImage, article.Category, article.IsPublished,
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.FindByID(ctx, id)
}

func (r *newsRepository) Update(ctx context.Context, id string, patch NewsUpdate) (*domain.NewsArticle, error) {
	set := []string{"updated_at = CURRENT_TIMESTAMP"}
	args := []any{}
	argN := 0
	add := func(col string, v any) {
		argN++
		args = append(args, v)
		set = append(set, col+" = $"+strconv.Itoa(argN))
	}

	if patch.Title != nil {
		add("title", *patch.Title)
	}
	if patch.Slug != nil {
		add("slug", *patch.Slug)
	}
	if patch.Summary != nil {
		add("summary", *patch.Summary)
	}
	if patch.Content != nil {
		add("content", *patch.Content)
	}
	if patch.CoverImage != nil {
		add("cover_image", *patch.CoverImage)
	}
	if patch.Category != nil {
		add("category", *patch.Category)
	}
	if patch.IsPublished != nil {
		add("is_published", *patch.IsPublished)
	}

	argN++
	args = append(args, id)
	query := "UPDATE news_articles SET " + strings.Join(set, ", ") + " WHERE id = $" + strconv.Itoa(argN)

	tag, err := r.db.Exec(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNewsNotFound
	}
	return r.FindByID(ctx, id)
}

func (r *newsRepository) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM news_articles WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNewsNotFound
	}
	return nil
}
