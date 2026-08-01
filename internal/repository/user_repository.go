package repository

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
)

var ErrUserNotFound = errors.New("user not found")

// UserListFilter narrows GET /admin/users queries.
type UserListFilter struct {
	Role   string
	Search string
	Page   int
	Limit  int
}

type UserRepository interface {
	CreateUser(ctx context.Context, user *domain.User) error
	FindByEmail(ctx context.Context, email string) (*domain.User, error)
	FindByID(ctx context.Context, id string) (*domain.User, error)
	UpdateProfile(ctx context.Context, id, fullName, phone, nationalID string) (*domain.User, error)
	UpdatePasswordHash(ctx context.Context, id, passwordHash string) error
	MarkVerified(ctx context.Context, email string) (*domain.User, error)
	ListUsers(ctx context.Context, filter UserListFilter) ([]*domain.User, int, error)
	UpdateRole(ctx context.Context, id string, role domain.UserRole) (*domain.User, error)
	UpdateSuspended(ctx context.Context, id string, suspended bool) (*domain.User, error)
}

type userRepository struct {
	db *pgxpool.Pool
}

func NewUserRepository(db *pgxpool.Pool) UserRepository {
	return &userRepository{db: db}
}

const userColumns = `id, email, password_hash, full_name, phone, national_id, role, member_tier, is_verified, is_suspended, created_at, updated_at`

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
	query := "SELECT " + userColumns + " FROM users WHERE email = $1"
	return scanUser(r.db.QueryRow(ctx, query, email))
}

func (r *userRepository) FindByID(ctx context.Context, id string) (*domain.User, error) {
	query := "SELECT " + userColumns + " FROM users WHERE id = $1"
	return scanUser(r.db.QueryRow(ctx, query, id))
}

func (r *userRepository) UpdateProfile(ctx context.Context, id, fullName, phone, nationalID string) (*domain.User, error) {
	query := `
		UPDATE users SET full_name = NULLIF($2, ''), phone = $3, national_id = $4, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
		RETURNING ` + userColumns
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
		RETURNING ` + userColumns
	return scanUser(r.db.QueryRow(ctx, query, email))
}

// ListUsers powers the admin user directory table: optional role/search filters, paginated,
// newest accounts first.
func (r *userRepository) ListUsers(ctx context.Context, filter UserListFilter) ([]*domain.User, int, error) {
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

	if filter.Role != "" {
		where += " AND role = " + nextArg(filter.Role)
	}
	if filter.Search != "" {
		p := nextArg("%" + filter.Search + "%")
		where += " AND (full_name ILIKE " + p + " OR email ILIKE " + p + ")"
	}

	var total int
	if err := r.db.QueryRow(ctx, "SELECT count(*) FROM users "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArg := nextArg(limit)
	offsetArg := nextArg(offset)
	query := "SELECT " + userColumns + " FROM users " + where +
		" ORDER BY created_at DESC LIMIT " + limitArg + " OFFSET " + offsetArg

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	users := make([]*domain.User, 0)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

func (r *userRepository) UpdateRole(ctx context.Context, id string, role domain.UserRole) (*domain.User, error) {
	query := "UPDATE users SET role = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1 RETURNING " + userColumns
	return scanUser(r.db.QueryRow(ctx, query, id, role))
}

func (r *userRepository) UpdateSuspended(ctx context.Context, id string, suspended bool) (*domain.User, error) {
	query := "UPDATE users SET is_suspended = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1 RETURNING " + userColumns
	return scanUser(r.db.QueryRow(ctx, query, id, suspended))
}

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	var fullName, phone, nationalID *string
	if err := row.Scan(
		&u.ID, &u.Email, &u.PasswordHash, &fullName, &phone, &nationalID,
		&u.Role, &u.MemberTier, &u.IsVerified, &u.IsSuspended, &u.CreatedAt, &u.UpdatedAt,
	); err != nil {
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
