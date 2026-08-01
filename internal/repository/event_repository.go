package repository

import (
	"context"
	"errors"
	"strconv"

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

// AdminEventListFilter narrows GET /admin/events queries.
type AdminEventListFilter struct {
	Status string
	Page   int
	Limit  int
}

type EventRepository interface {
	// RequiresIDVerification reports whether an event demands a national ID / passport
	// on file before reservation. Events without a matching row (e.g. not yet synced to
	// Postgres) are treated as not requiring verification.
	RequiresIDVerification(ctx context.Context, eventID string) (bool, error)
	ListActiveEvents(ctx context.Context) ([]*domain.EventSummary, error)
	FindEventByID(ctx context.Context, id string) (*domain.EventDetail, error)
	CreateEventWithZones(ctx context.Context, venueID, title, description, bannerURL string, eventDate string, status domain.EventStatus, zones []NewZone) (*domain.EventDetail, error)
	// FindZoneName resolves a seat_zones.id to its display name (e.g. "VIP Standing") for
	// e-ticket email rendering. Returns "" (no error) if zoneID doesn't match a row — orders
	// created via the /tickets/reserve dev fallback path may carry a placeholder zone id.
	FindZoneName(ctx context.Context, zoneID string) (string, error)
	// ListAdminEvents returns every event regardless of status, with sold-ticket-count and
	// gross-revenue aggregated from paid orders (not the seed-time seat_zones column).
	ListAdminEvents(ctx context.Context, filter AdminEventListFilter) ([]*domain.AdminEventSummary, int, error)
	// UpdateEventZones upserts the given zones for eventID (matched by zone_name, which is
	// unique per event) inside a single transaction. Zones not present in the caller's list
	// are left untouched — this never deletes a zone, since tickets FK to seat_zones and a
	// delete would cascade. Returns the event's full zone list after the upsert.
	UpdateEventZones(ctx context.Context, eventID string, zones []NewZone) ([]domain.Zone, error)
	// UpdateEventStatus transitions an event to a new lifecycle status. ADMIN only.
	UpdateEventStatus(ctx context.Context, eventID string, status domain.EventStatus) (*domain.EventDetail, error)
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

func (r *eventRepository) FindZoneName(ctx context.Context, zoneID string) (string, error) {
	var name string
	err := r.db.QueryRow(ctx, `SELECT zone_name FROM seat_zones WHERE id = $1`, zoneID).Scan(&name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return name, nil
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

func (r *eventRepository) ListAdminEvents(ctx context.Context, filter AdminEventListFilter) ([]*domain.AdminEventSummary, int, error) {
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

	if filter.Status != "" {
		where += " AND e.status = " + nextArg(filter.Status)
	}

	var total int
	if err := r.db.QueryRow(ctx, "SELECT count(*) FROM events e "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArg := nextArg(limit)
	offsetArg := nextArg(offset)
	query := `
		SELECT e.id, e.title, e.poster_url, e.event_date, e.status, v.name, v.address,
		       COALESCE(zt.total_capacity, 0), COALESCE(ot.tickets_sold, 0), COALESCE(ot.revenue, 0)
		FROM events e
		JOIN venues v ON e.venue_id = v.id
		LEFT JOIN (
			SELECT event_id, SUM(total_capacity) AS total_capacity
			FROM seat_zones GROUP BY event_id
		) zt ON zt.event_id = e.id
		LEFT JOIN (
			SELECT event_id, SUM(quantity) AS tickets_sold, SUM(total_amount) AS revenue
			FROM orders WHERE status IN ('COMPLETED', 'CHECKED_IN') GROUP BY event_id
		) ot ON ot.event_id = e.id
	` + where + `
		ORDER BY e.event_date DESC
		LIMIT ` + limitArg + ` OFFSET ` + offsetArg

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	events := make([]*domain.AdminEventSummary, 0)
	for rows.Next() {
		var e domain.AdminEventSummary
		var bannerURL *string
		if err := rows.Scan(
			&e.ID, &e.Title, &bannerURL, &e.EventDate, &e.Status, &e.VenueName, &e.VenueLocation,
			&e.TotalCapacity, &e.TicketsSold, &e.Revenue,
		); err != nil {
			return nil, 0, err
		}
		if bannerURL != nil {
			e.BannerURL = *bannerURL
		}
		events = append(events, &e)
	}
	return events, total, rows.Err()
}

func (r *eventRepository) UpdateEventZones(ctx context.Context, eventID string, zones []NewZone) ([]domain.Zone, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	for _, z := range zones {
		if _, err := tx.Exec(ctx, `
			INSERT INTO seat_zones (event_id, zone_name, seat_type, price, total_capacity, available_stock)
			VALUES ($1, $2, $3, $4, $5, $5)
			ON CONFLICT (event_id, zone_name) DO UPDATE
			SET seat_type = EXCLUDED.seat_type, price = EXCLUDED.price, total_capacity = EXCLUDED.total_capacity
		`, eventID, z.Name, z.Type, z.Price, z.TotalCapacity); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return r.findZonesByEventID(ctx, eventID)
}

func (r *eventRepository) UpdateEventStatus(ctx context.Context, eventID string, status domain.EventStatus) (*domain.EventDetail, error) {
	tag, err := r.db.Exec(ctx, `UPDATE events SET status = $2, updated_at = NOW() WHERE id = $1`, eventID, status)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrEventNotFound
	}
	return r.FindEventByID(ctx, eventID)
}
