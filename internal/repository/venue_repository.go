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

var (
	ErrVenueNotFound = errors.New("venue not found")
	// ErrVenueHasEvents blocks deletion — events.venue_id is ON DELETE CASCADE, so an
	// unguarded delete here would silently wipe out every event at that venue.
	ErrVenueHasEvents = errors.New("venue has existing events and cannot be deleted")
)

// VenueListFilter narrows GET /admin/venues queries.
type VenueListFilter struct {
	Search string
	Page   int
	Limit  int
}

type NewVenue struct {
	Name     string
	Address  string
	City     string
	Capacity int
	MapURL   string
}

type VenueUpdate struct {
	Name     *string
	Address  *string
	City     *string
	Capacity *int
	MapURL   *string
}

type VenueRepository interface {
	ListVenues(ctx context.Context, filter VenueListFilter) ([]*domain.Venue, int, error)
	FindByID(ctx context.Context, id string) (*domain.Venue, error)
	Create(ctx context.Context, v NewVenue) (*domain.Venue, error)
	Update(ctx context.Context, id string, patch VenueUpdate) (*domain.Venue, error)
	Delete(ctx context.Context, id string) error
}

type venueRepository struct {
	db *pgxpool.Pool
}

func NewVenueRepository(db *pgxpool.Pool) VenueRepository {
	return &venueRepository{db: db}
}

const venueColumns = `id, name, address, capacity, city, map_url`

func scanVenue(row pgx.Row) (*domain.Venue, error) {
	var v domain.Venue
	var city, mapURL *string
	if err := row.Scan(&v.ID, &v.Name, &v.Location, &v.Capacity, &city, &mapURL); err != nil {
		return nil, err
	}
	if city != nil {
		v.City = *city
	}
	if mapURL != nil {
		v.MapURL = *mapURL
	}
	return &v, nil
}

func (r *venueRepository) ListVenues(ctx context.Context, filter VenueListFilter) ([]*domain.Venue, int, error) {
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
		p := nextArg("%" + filter.Search + "%")
		where += " AND (name ILIKE " + p + " OR city ILIKE " + p + ")"
	}

	var total int
	if err := r.db.QueryRow(ctx, "SELECT count(*) FROM venues "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArg := nextArg(limit)
	offsetArg := nextArg(offset)
	query := "SELECT " + venueColumns + " FROM venues " + where +
		" ORDER BY name ASC LIMIT " + limitArg + " OFFSET " + offsetArg

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	venues := make([]*domain.Venue, 0)
	for rows.Next() {
		v, err := scanVenue(rows)
		if err != nil {
			return nil, 0, err
		}
		venues = append(venues, v)
	}
	return venues, total, rows.Err()
}

func (r *venueRepository) FindByID(ctx context.Context, id string) (*domain.Venue, error) {
	v, err := scanVenue(r.db.QueryRow(ctx, "SELECT "+venueColumns+" FROM venues WHERE id = $1", id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrVenueNotFound
		}
		return nil, err
	}
	return v, nil
}

func (r *venueRepository) Create(ctx context.Context, v NewVenue) (*domain.Venue, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO venues (name, address, capacity, city, map_url)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''))
		RETURNING id
	`, v.Name, v.Address, v.Capacity, v.City, v.MapURL).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.FindByID(ctx, id)
}

func (r *venueRepository) Update(ctx context.Context, id string, patch VenueUpdate) (*domain.Venue, error) {
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
	if patch.Address != nil {
		add("address", *patch.Address)
	}
	if patch.City != nil {
		add("city", *patch.City)
	}
	if patch.Capacity != nil {
		add("capacity", *patch.Capacity)
	}
	if patch.MapURL != nil {
		add("map_url", *patch.MapURL)
	}
	if len(set) == 0 {
		return r.FindByID(ctx, id)
	}

	argN++
	args = append(args, id)
	query := "UPDATE venues SET " + strings.Join(set, ", ") + " WHERE id = $" + strconv.Itoa(argN)

	tag, err := r.db.Exec(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrVenueNotFound
	}
	return r.FindByID(ctx, id)
}

func (r *venueRepository) Delete(ctx context.Context, id string) error {
	var eventCount int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM events WHERE venue_id = $1`, id).Scan(&eventCount); err != nil {
		return err
	}
	if eventCount > 0 {
		return ErrVenueHasEvents
	}

	tag, err := r.db.Exec(ctx, `DELETE FROM venues WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrVenueNotFound
	}
	return nil
}
