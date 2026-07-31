package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
)

var ErrEventNotFound = errors.New("event not found")

type NewZone struct {
	Name          string
	Type          string
	Price         float64
	TotalCapacity int
}

type EventRepository interface {
	// RequiresIDVerification reports whether an event demands a national ID / passport
	// on file before reservation. Events without a matching row (e.g. not yet synced to
	// Postgres) are treated as not requiring verification.
	RequiresIDVerification(ctx context.Context, eventID string) (bool, error)
	ListActiveEvents(ctx context.Context) ([]*domain.EventSummary, error)
	FindEventByID(ctx context.Context, id string) (*domain.EventDetail, error)
	CreateEventWithZones(ctx context.Context, venueID, title, description, bannerURL string, eventDate string, status domain.EventStatus, zones []NewZone) (*domain.EventDetail, error)
}

type eventRepository struct {
	db *pgxpool.Pool
}

func NewEventRepository(db *pgxpool.Pool) EventRepository {
	return &eventRepository{db: db}
}

func (r *eventRepository) RequiresIDVerification(ctx context.Context, eventID string) (bool, error) {
	var requires bool
	err := r.db.QueryRow(ctx, `SELECT requires_id_verification FROM events WHERE id = $1`, eventID).Scan(&requires)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return requires, nil
}

func (r *eventRepository) ListActiveEvents(ctx context.Context) ([]*domain.EventSummary, error) {
	query := `
		SELECT e.id, e.title, e.poster_url, e.event_date, e.status, v.name, v.address,
		       COALESCE(MIN(z.price), 0) AS min_price
		FROM events e
		JOIN venues v ON e.venue_id = v.id
		LEFT JOIN seat_zones z ON z.event_id = e.id
		WHERE e.status != 'ENDED'
		GROUP BY e.id, v.name, v.address
		ORDER BY e.event_date ASC
	`
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]*domain.EventSummary, 0)
	for rows.Next() {
		var e domain.EventSummary
		var bannerURL *string
		if err := rows.Scan(&e.ID, &e.Title, &bannerURL, &e.EventDate, &e.Status, &e.VenueName, &e.VenueLocation, &e.MinPrice); err != nil {
			return nil, err
		}
		if bannerURL != nil {
			e.BannerURL = *bannerURL
		}
		events = append(events, &e)
	}
	return events, rows.Err()
}

func (r *eventRepository) FindEventByID(ctx context.Context, id string) (*domain.EventDetail, error) {
	query := `
		SELECT e.id, e.title, e.description, e.poster_url, e.event_date, e.status,
		       v.id, v.name, v.address, v.capacity
		FROM events e
		JOIN venues v ON e.venue_id = v.id
		WHERE e.id = $1
	`
	var d domain.EventDetail
	var description, bannerURL *string
	err := r.db.QueryRow(ctx, query, id).Scan(
		&d.ID, &d.Title, &description, &bannerURL, &d.EventDate, &d.Status,
		&d.Venue.ID, &d.Venue.Name, &d.Venue.Location, &d.Venue.Capacity,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEventNotFound
		}
		return nil, err
	}
	if description != nil {
		d.Description = *description
	}
	if bannerURL != nil {
		d.BannerURL = *bannerURL
	}

	zones, err := r.findZonesByEventID(ctx, id)
	if err != nil {
		return nil, err
	}
	d.Zones = zones

	return &d, nil
}

func (r *eventRepository) findZonesByEventID(ctx context.Context, eventID string) ([]domain.Zone, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, zone_name, seat_type, price, total_capacity, available_stock
		FROM seat_zones WHERE event_id = $1
		ORDER BY price DESC
	`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	zones := make([]domain.Zone, 0)
	for rows.Next() {
		var z domain.Zone
		if err := rows.Scan(&z.ID, &z.Name, &z.Type, &z.Price, &z.TotalCapacity, &z.AvailableSeats); err != nil {
			return nil, err
		}
		zones = append(zones, z)
	}
	return zones, rows.Err()
}

func (r *eventRepository) CreateEventWithZones(
	ctx context.Context,
	venueID, title, description, bannerURL string,
	eventDate string,
	status domain.EventStatus,
	zones []NewZone,
) (*domain.EventDetail, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var eventID string
	err = tx.QueryRow(ctx, `
		INSERT INTO events (venue_id, title, description, poster_url, event_date, sale_start_date, sale_end_date, status)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, CURRENT_TIMESTAMP, $5, $6)
		RETURNING id
	`, venueID, title, description, bannerURL, eventDate, status).Scan(&eventID)
	if err != nil {
		return nil, err
	}

	for _, z := range zones {
		if _, err := tx.Exec(ctx, `
			INSERT INTO seat_zones (event_id, zone_name, seat_type, price, total_capacity, available_stock)
			VALUES ($1, $2, $3, $4, $5, $5)
		`, eventID, z.Name, z.Type, z.Price, z.TotalCapacity); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return r.FindEventByID(ctx, eventID)
}
