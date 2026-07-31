package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
)

var (
	ErrOrderNotFound    = errors.New("order not found")
	ErrOrderNotEligible = errors.New("order is not eligible for check-in")
	ErrOrderAlreadyIn   = errors.New("ticket has already been checked in")
)

type OrderRepository interface {
	FindOrdersByUserID(ctx context.Context, userID string) ([]*domain.Order, error)
	VerifyAndCheckInTicket(ctx context.Context, orderID string) (*domain.Order, error)
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
		UPDATE orders SET status = 'CHECKED_IN', checked_in_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status = 'COMPLETED'
		RETURNING id, user_id, event_id, zone_id, quantity, total_amount, status, created_at, checked_in_at
	`
	var o domain.Order
	var zoneID *string
	var checkedInAt *time.Time
	err := r.db.QueryRow(ctx, query, orderID).Scan(
		&o.ID, &o.UserID, &o.EventID, &zoneID, &o.Quantity, &o.TotalAmount, &o.Status, &o.CreatedAt, &checkedInAt,
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

	// Update didn't match — figure out why so we can report a precise error.
	var status domain.OrderStatus
	lookupErr := r.db.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, orderID).Scan(&status)
	if errors.Is(lookupErr, pgx.ErrNoRows) {
		return nil, ErrOrderNotFound
	}
	if lookupErr != nil {
		return nil, lookupErr
	}
	if status == domain.OrderCheckedIn {
		return nil, ErrOrderAlreadyIn
	}
	return nil, ErrOrderNotEligible
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
