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
	UpdateProfile(ctx context.Context, id, fullName, phone, nationalID string) (*domain.User, error)
	UpdatePasswordHash(ctx context.Context, id, passwordHash string) error
	MarkVerified(ctx context.Context, email string) (*domain.User, error)
}

type userRepository struct {
	db *pgxpool.Pool
}

func NewUserRepository(db *pgxpool.Pool) UserRepository {
	return &userRepository{db: db}
}

func (r *userRepository) CreateUser(ctx context.Context, user *domain.User) error {
	query := `
		INSERT INTO users (email, password_hash, full_name, phone, national_id, role, member_tier, is_verified)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $7, $8)
		RETURNING id, role, member_tier, created_at, updated_at
	`
	return r.db.QueryRow(ctx, query,
		user.Email, user.PasswordHash, user.FullName, user.Phone, user.NationalID, user.Role, user.MemberTier, user.IsVerified,
	).Scan(&user.ID, &user.Role, &user.MemberTier, &user.CreatedAt, &user.UpdatedAt)
}

func (r *userRepository) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	query := `
		SELECT id, email, password_hash, full_name, phone, national_id, role, member_tier, is_verified, created_at, updated_at
		FROM users WHERE email = $1
	`
	return scanUser(r.db.QueryRow(ctx, query, email))
}

func (r *userRepository) FindByID(ctx context.Context, id string) (*domain.User, error) {
	query := `
		SELECT id, email, password_hash, full_name, phone, national_id, role, member_tier, is_verified, created_at, updated_at
		FROM users WHERE id = $1
	`
	return scanUser(r.db.QueryRow(ctx, query, id))
}

func (r *userRepository) UpdateProfile(ctx context.Context, id, fullName, phone, nationalID string) (*domain.User, error) {
	query := `
		UPDATE users SET full_name = NULLIF($2, ''), phone = $3, national_id = $4, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
		RETURNING id, email, password_hash, full_name, phone, national_id, role, member_tier, is_verified, created_at, updated_at
	`
	return scanUser(r.db.QueryRow(ctx, query, id, fullName, phone, nationalID))
}

func (r *userRepository) UpdatePasswordHash(ctx context.Context, id, passwordHash string) error {
	_, err := r.db.Exec(ctx, `UPDATE users SET password_hash = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1`, id, passwordHash)
	return err
}

func (r *userRepository) MarkVerified(ctx context.Context, email string) (*domain.User, error) {
	query := `
		UPDATE users SET is_verified = TRUE, updated_at = CURRENT_TIMESTAMP
		WHERE email = $1
		RETURNING id, email, password_hash, full_name, phone, national_id, role, member_tier, is_verified, created_at, updated_at
	`
	return scanUser(r.db.QueryRow(ctx, query, email))
}

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	var fullName, phone, nationalID *string
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &fullName, &phone, &nationalID, &u.Role, &u.MemberTier, &u.IsVerified, &u.CreatedAt, &u.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if fullName != nil {
		u.FullName = *fullName
	}
	if phone != nil {
		u.Phone = *phone
	}
	if nationalID != nil {
		u.NationalID = *nationalID
	}
	return &u, nil
}
