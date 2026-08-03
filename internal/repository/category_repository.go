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

var ErrCategoryNotFound = errors.New("category not found")

// CategoryListFilter narrows GET /admin/categories queries.
type CategoryListFilter struct {
	Search string
	Page   int
	Limit  int
}

type NewCategory struct {
	Name string
	Slug string
}

type CategoryUpdate struct {
	Name *string
	Slug *string
}

type CategoryRepository interface {
	ListCategories(ctx context.Context, filter CategoryListFilter) ([]*domain.Category, int, error)
	FindByID(ctx context.Context, id string) (*domain.Category, error)
	SlugExists(ctx context.Context, slug string) (bool, error)
	Create(ctx context.Context, cat NewCategory) (*domain.Category, error)
	Update(ctx context.Context, id string, patch CategoryUpdate) (*domain.Category, error)
	Delete(ctx context.Context, id string) error
}

type categoryRepository struct {
	db *pgxpool.Pool
}

func NewCategoryRepository(db *pgxpool.Pool) CategoryRepository {
	return &categoryRepository{db: db}
}

const categoryColumns = `id, name, slug, created_at`

func scanCategory(row pgx.Row) (*domain.Category, error) {
	var cat domain.Category
	if err := row.Scan(&cat.ID, &cat.Name, &cat.Slug, &cat.CreatedAt); err != nil {
		return nil, err
	}
	return &cat, nil
}

func (r *categoryRepository) ListCategories(ctx context.Context, filter CategoryListFilter) ([]*domain.Category, int, error) {
	page := max(filter.Page, 1)
	limit := filter.Limit
	if limit < 1 || limit > 100 {
		limit = 20
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

	if filter.Search != "" {
		where += " AND name ILIKE " + nextArg("%"+filter.Search+"%")
	}

	var total int
	if err := r.db.QueryRow(ctx, "SELECT count(*) FROM categories "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArg := nextArg(limit)
	offsetArg := nextArg(offset)
	query := "SELECT " + categoryColumns + " FROM categories " + where +
		" ORDER BY name ASC LIMIT " + limitArg + " OFFSET " + offsetArg

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	categories := make([]*domain.Category, 0)
	for rows.Next() {
		cat, err := scanCategory(rows)
		if err != nil {
			return nil, 0, err
		}
		categories = append(categories, cat)
	}
	return categories, total, rows.Err()
}

func (r *categoryRepository) FindByID(ctx context.Context, id string) (*domain.Category, error) {
	cat, err := scanCategory(r.db.QueryRow(ctx, "SELECT "+categoryColumns+" FROM categories WHERE id = $1", id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCategoryNotFound
		}
		return nil, err
	}
	return cat, nil
}

func (r *categoryRepository) SlugExists(ctx context.Context, slug string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM categories WHERE slug = $1)`, slug).Scan(&exists)
	return exists, err
}

func (r *categoryRepository) Create(ctx context.Context, cat NewCategory) (*domain.Category, error) {
	var id string
	err := r.db.QueryRow(ctx, `INSERT INTO categories (name, slug) VALUES ($1, $2) RETURNING id`, cat.Name, cat.Slug).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.FindByID(ctx, id)
}

func (r *categoryRepository) Update(ctx context.Context, id string, patch CategoryUpdate) (*domain.Category, error) {
	set := []string{}
	args := []any{}
	argN := 0
	add := func(col string, v any) {
		argN++
		args = append(args, v)
		set = append(set, col+" = $"+strconv.Itoa(argN))
	}

	if patch.Name != nil {
		add("name", *patch.Name)
	}
	if patch.Slug != nil {
		add("slug", *patch.Slug)
	}
	if len(set) == 0 {
		return r.FindByID(ctx, id)
	}

	argN++
	args = append(args, id)
	query := "UPDATE categories SET " + strings.Join(set, ", ") + " WHERE id = $" + strconv.Itoa(argN)

	tag, err := r.db.Exec(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrCategoryNotFound
	}
	return r.FindByID(ctx, id)
}

func (r *categoryRepository) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM categories WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCategoryNotFound
	}
	return nil
}
