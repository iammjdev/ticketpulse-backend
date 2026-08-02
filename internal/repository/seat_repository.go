package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
)

var ErrSeatNotFound = errors.New("seat not found")

type NewSeat struct {
	ZoneID     string
	RowLabel   string
	SeatNumber int
	PositionX  float64
	PositionY  float64
}

type SeatRepository interface {
	GetSeatsByEventID(ctx context.Context, eventID string) ([]domain.Seat, error)
	// BulkCreateSeats inserts (or upserts, keyed by the event_id/row_label/seat_number
	// unique constraint) the seat map layout for an event.
	BulkCreateSeats(ctx context.Context, eventID string, seats []NewSeat) error
	// FindSeatForReservation returns the seat and its zone's ticket price, scoped to eventID
	// so a seat belonging to a different event can't be reserved through this lookup.
	FindSeatForReservation(ctx context.Context, eventID, seatID string) (*domain.Seat, float64, error)
}

type seatRepository struct {
	db *pgxpool.Pool
}

func NewSeatRepository(db *pgxpool.Pool) SeatRepository {
	return &seatRepository{db: db}
}

func (r *seatRepository) GetSeatsByEventID(ctx context.Context, eventID string) ([]domain.Seat, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, zone_id, event_id, row_label, seat_number, position_x, position_y
		FROM seats WHERE event_id = $1
		ORDER BY row_label, seat_number
	`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seats := make([]domain.Seat, 0)
	for rows.Next() {
		var s domain.Seat
		if err := rows.Scan(&s.ID, &s.ZoneID, &s.EventID, &s.RowLabel, &s.SeatNumber, &s.PositionX, &s.PositionY); err != nil {
			return nil, err
		}
		seats = append(seats, s)
	}
	return seats, rows.Err()
}

func (r *seatRepository) BulkCreateSeats(ctx context.Context, eventID string, seats []NewSeat) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	batch := &pgx.Batch{}
	for _, s := range seats {
		batch.Queue(`
			INSERT INTO seats (zone_id, event_id, row_label, seat_number, position_x, position_y)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (event_id, row_label, seat_number) DO UPDATE
			SET zone_id = EXCLUDED.zone_id, position_x = EXCLUDED.position_x, position_y = EXCLUDED.position_y
		`, s.ZoneID, eventID, s.RowLabel, s.SeatNumber, s.PositionX, s.PositionY)
	}

	br := tx.SendBatch(ctx, batch)
	for range seats {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return err
		}
	}
	if err := br.Close(); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *seatRepository) FindSeatForReservation(ctx context.Context, eventID, seatID string) (*domain.Seat, float64, error) {
	var s domain.Seat
	var price float64
	err := r.db.QueryRow(ctx, `
		SELECT s.id, s.zone_id, s.event_id, s.row_label, s.seat_number, s.position_x, s.position_y, z.price
		FROM seats s
		JOIN seat_zones z ON z.id = s.zone_id
		WHERE s.id = $1 AND s.event_id = $2
	`, seatID, eventID).Scan(&s.ID, &s.ZoneID, &s.EventID, &s.RowLabel, &s.SeatNumber, &s.PositionX, &s.PositionY, &price)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, ErrSeatNotFound
		}
		return nil, 0, err
	}
	return &s, price, nil
}
