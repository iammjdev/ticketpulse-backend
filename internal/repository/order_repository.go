package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
)

var (
	ErrOrderNotFound    = errors.New("order not found")
	ErrOrderNotEligible = errors.New("order is not eligible for check-in")
	ErrOrderAlreadyIn   = errors.New("ticket has already been checked in")
	ErrOrderCancelled   = errors.New("ticket has been cancelled")
)

// AdminOrderListFilter narrows GET /admin/orders queries.
type AdminOrderListFilter struct {
	Status string
	Page   int
	Limit  int
}

type OrderRepository interface {
	FindOrdersByUserID(ctx context.Context, userID string) ([]*domain.Order, error)
	VerifyAndCheckInTicket(ctx context.Context, orderID string) (*domain.Order, error)
	FindByID(ctx context.Context, id string) (*domain.Order, error)
	// MarkPaid transitions PENDING -> COMPLETED. Idempotent: re-calling on an already-paid
	// order is a no-op that just returns its current (already COMPLETED) state, so a retried
	// webhook delivery never errors.
	MarkPaid(ctx context.Context, id string) (*domain.Order, error)
	// CancelIfPending transitions PENDING -> CANCELLED. wasCancelled is false when the order
	// had already left PENDING (paid, or cancelled by a prior call) — callers use that to
	// avoid double-restoring Redis stock.
	CancelIfPending(ctx context.Context, id string) (order *domain.Order, wasCancelled bool, err error)
	// AdminStats aggregates gross revenue, tickets sold, and checked-in count across every paid
	// order (COMPLETED or CHECKED_IN), platform-wide, for the admin stats dashboard.
	AdminStats(ctx context.Context) (revenue float64, ticketsSold int, checkedIn int, err error)
	// ListAdminOrders returns every order regardless of user, joined with the placing user and
	// event, optionally filtered by status and paginated. ADMIN only.
	ListAdminOrders(ctx context.Context, filter AdminOrderListFilter) ([]*domain.AdminOrderSummary, int, error)
	// GateScanVelocity counts orders checked in within the trailing 60 seconds — a live
	// scans-per-minute reading for the admin dashboard's gate scan velocity KPI.
	GateScanVelocity(ctx context.Context) (int, error)
	// SalesSeries buckets gross revenue and tickets sold by date_trunc(granularity, created_at)
	// for every paid order since the given time, for the admin dashboard's sales chart.
	// granularity must be one of "hour", "day", "month".
	SalesSeries(ctx context.Context, since time.Time, granularity string) ([]domain.SalesSeriesPoint, error)
	// RangeTotals sums gross revenue and tickets sold for paid orders in [since, until) — used
	// to compute the dashboard's period-over-period delta.
	RangeTotals(ctx context.Context, since, until time.Time) (revenue float64, tickets int, err error)
	// ZoneBreakdown aggregates sold tickets, capacity, and revenue per zone_name across every
	// event, for the admin dashboard's zone occupancy & revenue share widget.
	ZoneBreakdown(ctx context.Context) ([]domain.ZoneBreakdownPoint, error)
}

type orderRepository struct {
	db *pgxpool.Pool
}

func NewOrderRepository(db *pgxpool.Pool) OrderRepository {
	return &orderRepository{db: db}
}

func (r *orderRepository) FindOrdersByUserID(ctx context.Context, userID string) ([]*domain.Order, error) {
	query := `
		SELECT o.id, o.user_id, o.event_id, e.title, v.name, e.event_date, e.poster_url,
		       o.zone_id, o.quantity, o.total_amount, o.status, o.created_at, o.checked_in_at
		FROM orders o
		JOIN events e ON o.event_id = e.id
		JOIN venues v ON e.venue_id = v.id
		WHERE o.user_id = $1
		ORDER BY o.created_at DESC
	`
	rows, err := r.db.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orders := make([]*domain.Order, 0)
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

func (r *orderRepository) VerifyAndCheckInTicket(ctx context.Context, orderID string) (*domain.Order, error) {
	query := `
		UPDATE orders o
		SET status = 'CHECKED_IN', checked_in_at = NOW(), updated_at = NOW()
		FROM events e
		WHERE o.id = $1 AND o.status = 'COMPLETED' AND o.event_id = e.id
		RETURNING o.id, o.user_id, o.event_id, e.title, o.zone_id, o.quantity, o.total_amount, o.status, o.created_at, o.checked_in_at
	`
	var o domain.Order
	var zoneID *string
	var checkedInAt *time.Time
	err := r.db.QueryRow(ctx, query, orderID).Scan(
		&o.ID, &o.UserID, &o.EventID, &o.EventTitle, &zoneID, &o.Quantity, &o.TotalAmount, &o.Status, &o.CreatedAt, &checkedInAt,
	)
	if err == nil {
		if zoneID != nil {
			o.ZoneID = *zoneID
		}
		o.CheckedInAt = checkedInAt
		return &o, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// Update didn't match — look the order up (joined with its event) so the handler can
	// still report checked_in_at / event details alongside the precise error.
	lookupQuery := `
		SELECT o.id, o.user_id, o.event_id, e.title, o.zone_id, o.quantity, o.total_amount, o.status, o.created_at, o.checked_in_at
		FROM orders o JOIN events e ON o.event_id = e.id
		WHERE o.id = $1
	`
	var o2 domain.Order
	var zoneID2 *string
	var checkedInAt2 *time.Time
	lookupErr := r.db.QueryRow(ctx, lookupQuery, orderID).Scan(
		&o2.ID, &o2.UserID, &o2.EventID, &o2.EventTitle, &zoneID2, &o2.Quantity, &o2.TotalAmount, &o2.Status, &o2.CreatedAt, &checkedInAt2,
	)
	if errors.Is(lookupErr, pgx.ErrNoRows) {
		return nil, ErrOrderNotFound
	}
	if lookupErr != nil {
		return nil, lookupErr
	}
	if zoneID2 != nil {
		o2.ZoneID = *zoneID2
	}
	o2.CheckedInAt = checkedInAt2

	switch o2.Status {
	case domain.OrderCheckedIn:
		return &o2, ErrOrderAlreadyIn
	case domain.OrderCancelled:
		return &o2, ErrOrderCancelled
	default:
		return &o2, ErrOrderNotEligible
	}
}

func (r *orderRepository) FindByID(ctx context.Context, id string) (*domain.Order, error) {
	query := `
		SELECT o.id, o.user_id, o.event_id, e.title, v.name, e.event_date, e.poster_url,
		       o.zone_id, o.quantity, o.total_amount, o.status, o.created_at, o.checked_in_at
		FROM orders o
		JOIN events e ON o.event_id = e.id
		JOIN venues v ON e.venue_id = v.id
		WHERE o.id = $1
	`
	o, err := scanOrder(r.db.QueryRow(ctx, query, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	return o, nil
}

func (r *orderRepository) MarkPaid(ctx context.Context, id string) (*domain.Order, error) {
	if _, err := r.db.Exec(ctx, `
		UPDATE orders SET status = 'COMPLETED', updated_at = NOW() WHERE id = $1 AND status = 'PENDING'
	`, id); err != nil {
		return nil, err
	}
	return r.FindByID(ctx, id)
}

func (r *orderRepository) CancelIfPending(ctx context.Context, id string) (*domain.Order, bool, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE orders SET status = 'CANCELLED', updated_at = NOW() WHERE id = $1 AND status = 'PENDING'
	`, id)
	if err != nil {
		return nil, false, err
	}
	order, err := r.FindByID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return order, tag.RowsAffected() > 0, nil
}

func (r *orderRepository) AdminStats(ctx context.Context) (revenue float64, ticketsSold int, checkedIn int, err error) {
	query := `
		SELECT COALESCE(SUM(total_amount), 0), COALESCE(SUM(quantity), 0),
		       COUNT(*) FILTER (WHERE status = 'CHECKED_IN')
		FROM orders
		WHERE status IN ('COMPLETED', 'CHECKED_IN')
	`
	err = r.db.QueryRow(ctx, query).Scan(&revenue, &ticketsSold, &checkedIn)
	return revenue, ticketsSold, checkedIn, err
}

func (r *orderRepository) GateScanVelocity(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM orders WHERE checked_in_at >= NOW() - INTERVAL '60 seconds'
	`).Scan(&count)
	return count, err
}

func (r *orderRepository) SalesSeries(ctx context.Context, since time.Time, granularity string) ([]domain.SalesSeriesPoint, error) {
	switch granularity {
	case "hour", "day", "month":
	default:
		return nil, fmt.Errorf("invalid granularity %q", granularity)
	}

	query := `
		SELECT date_trunc($1, created_at) AS bucket,
		       COALESCE(SUM(total_amount), 0) AS revenue,
		       COALESCE(SUM(quantity), 0) AS tickets
		FROM orders
		WHERE status IN ('COMPLETED', 'CHECKED_IN') AND created_at >= $2
		GROUP BY bucket
		ORDER BY bucket
	`
	rows, err := r.db.Query(ctx, query, granularity, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	points := make([]domain.SalesSeriesPoint, 0)
	for rows.Next() {
		var p domain.SalesSeriesPoint
		if err := rows.Scan(&p.Bucket, &p.Revenue, &p.TicketsSold); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

func (r *orderRepository) RangeTotals(ctx context.Context, since, until time.Time) (revenue float64, tickets int, err error) {
	query := `
		SELECT COALESCE(SUM(total_amount), 0), COALESCE(SUM(quantity), 0)
		FROM orders
		WHERE status IN ('COMPLETED', 'CHECKED_IN') AND created_at >= $1 AND created_at < $2
	`
	err = r.db.QueryRow(ctx, query, since, until).Scan(&revenue, &tickets)
	return revenue, tickets, err
}

func (r *orderRepository) ZoneBreakdown(ctx context.Context) ([]domain.ZoneBreakdownPoint, error) {
	query := `
		WITH zone_totals AS (
			SELECT zone_name, SUM(total_capacity) AS capacity
			FROM seat_zones
			GROUP BY zone_name
		),
		zone_sales AS (
			SELECT sz.zone_name, SUM(o.quantity) AS sold, SUM(o.total_amount) AS revenue
			FROM orders o
			JOIN seat_zones sz ON sz.id::text = o.zone_id
			WHERE o.status IN ('COMPLETED', 'CHECKED_IN')
			GROUP BY sz.zone_name
		)
		SELECT zt.zone_name, zt.capacity, COALESCE(zs.sold, 0), COALESCE(zs.revenue, 0)
		FROM zone_totals zt
		LEFT JOIN zone_sales zs ON zs.zone_name = zt.zone_name
		ORDER BY zt.zone_name
	`
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	zones := make([]domain.ZoneBreakdownPoint, 0)
	for rows.Next() {
		var z domain.ZoneBreakdownPoint
		if err := rows.Scan(&z.Name, &z.Capacity, &z.Sold, &z.Revenue); err != nil {
			return nil, err
		}
		zones = append(zones, z)
	}
	return zones, rows.Err()
}

func (r *orderRepository) ListAdminOrders(ctx context.Context, filter AdminOrderListFilter) ([]*domain.AdminOrderSummary, int, error) {
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
		where += " AND o.status = " + nextArg(filter.Status)
	}

	var total int
	if err := r.db.QueryRow(ctx, "SELECT count(*) FROM orders o "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArg := nextArg(limit)
	offsetArg := nextArg(offset)
	query := `
		SELECT o.id, o.user_id, u.email, u.full_name, o.event_id, e.title,
		       o.zone_id, sz.zone_name, o.quantity, o.total_amount, o.status, o.created_at, o.checked_in_at
		FROM orders o
		LEFT JOIN users u ON o.user_id = u.id
		LEFT JOIN events e ON o.event_id = e.id
		LEFT JOIN seat_zones sz ON sz.id::text = o.zone_id
	` + where + `
		ORDER BY o.created_at DESC
		LIMIT ` + limitArg + ` OFFSET ` + offsetArg

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	orders := make([]*domain.AdminOrderSummary, 0)
	for rows.Next() {
		var o domain.AdminOrderSummary
		var userEmail, userFullName, eventTitle, zoneID, zoneName *string
		var checkedInAt *time.Time
		if err := rows.Scan(
			&o.ID, &o.UserID, &userEmail, &userFullName, &o.EventID, &eventTitle,
			&zoneID, &zoneName, &o.Quantity, &o.TotalAmount, &o.Status, &o.CreatedAt, &checkedInAt,
		); err != nil {
			return nil, 0, err
		}
		if userEmail != nil {
			o.UserEmail = *userEmail
		}
		if userFullName != nil {
			o.UserFullName = *userFullName
		}
		if eventTitle != nil {
			o.EventTitle = *eventTitle
		}
		if zoneID != nil {
			o.ZoneID = *zoneID
		}
		if zoneName != nil {
			o.ZoneName = *zoneName
		}
		o.CheckedInAt = checkedInAt
		orders = append(orders, &o)
	}
	return orders, total, rows.Err()
}

func scanOrder(row pgx.Row) (*domain.Order, error) {
	var o domain.Order
	var posterURL *string
	var zoneID *string
	var checkedInAt *time.Time
	if err := row.Scan(
		&o.ID, &o.UserID, &o.EventID, &o.EventTitle, &o.VenueName, &o.EventDate, &posterURL,
		&zoneID, &o.Quantity, &o.TotalAmount, &o.Status, &o.CreatedAt, &checkedInAt,
	); err != nil {
		return nil, err
	}
	if posterURL != nil {
		o.PosterURL = *posterURL
	}
	if zoneID != nil {
		o.ZoneID = *zoneID
	}
	o.CheckedInAt = checkedInAt
	return &o, nil
}
