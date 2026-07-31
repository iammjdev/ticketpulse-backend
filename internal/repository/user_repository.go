package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
)

var ErrUserNotFound = errors.New("user not found")

type UserRepository interface {
	CreateUser(ctx context.Context, user *domain.User) error
	FindByEmail(ctx context.Context, email string) (*domain.User, error)
	FindByID(ctx context.Context, id string) (*domain.User, error)
}

type userRepository struct {
	db *pgxpool.Pool
}

func NewUserRepository(db *pgxpool.Pool) UserRepository {
	return &userRepository{db: db}
}

func (r *userRepository) CreateUser(ctx context.Context, user *domain.User) error {
	query := `
		INSERT INTO users (email, password_hash, full_name, phone, national_id, role, member_tier)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, role, member_tier, created_at, updated_at
	`
	return r.db.QueryRow(ctx, query,
		user.Email, user.PasswordHash, user.FullName, user.Phone, user.NationalID, user.Role, user.MemberTier,
	).Scan(&user.ID, &user.Role, &user.MemberTier, &user.CreatedAt, &user.UpdatedAt)
}

func (r *userRepository) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	query := `
		SELECT id, email, password_hash, full_name, phone, national_id, role, member_tier, created_at, updated_at
		FROM users WHERE email = $1
	`
	return scanUser(r.db.QueryRow(ctx, query, email))
}

func (r *userRepository) FindByID(ctx context.Context, id string) (*domain.User, error) {
	query := `
		SELECT id, email, password_hash, full_name, phone, national_id, role, member_tier, created_at, updated_at
		FROM users WHERE id = $1
	`
	return scanUser(r.db.QueryRow(ctx, query, id))
}

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	var phone, nationalID *string
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.FullName, &phone, &nationalID, &u.Role, &u.MemberTier, &u.CreatedAt, &u.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if phone != nil {
		u.Phone = *phone
	}
	if nationalID != nil {
		u.NationalID = *nationalID
	}
	return &u, nil
}
