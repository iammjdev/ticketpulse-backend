package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type EventRepository interface {
	// RequiresIDVerification reports whether an event demands a national ID / passport
	// on file before reservation. Events without a matching row (e.g. not yet synced to
	// Postgres) are treated as not requiring verification.
	RequiresIDVerification(ctx context.Context, eventID string) (bool, error)
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
