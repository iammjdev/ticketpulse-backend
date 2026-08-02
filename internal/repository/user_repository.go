package repository

import (
	"context"
	"encoding/json"
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
	// ListUsers powers the admin directory table. Every returned user's LastLoginAt, OrderCount,
	// and LoginEvents (last 5) are populated for real from login_events/orders — not left zero.
	ListUsers(ctx context.Context, filter UserListFilter) ([]*domain.User, int, error)
	UpdateRole(ctx context.Context, id string, role domain.UserRole) (*domain.User, error)
	UpdateSuspended(ctx context.Context, id string, suspended bool) (*domain.User, error)
	// RecordLoginEvent appends one row to the login audit trail, success or failure.
	RecordLoginEvent(ctx context.Context, userID, ipAddress, userAgent string, success bool) error
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
// newest accounts first. Each row also carries LastLoginAt, OrderCount, and the last 5
// LoginEvents, aggregated in-query via LATERAL joins rather than N+1 round trips.
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
	if err := r.db.QueryRow(ctx, "SELECT count(*) FROM users u "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArg := nextArg(limit)
	offsetArg := nextArg(offset)
	query := `
		SELECT u.id, u.email, u.password_hash, u.full_name, u.phone, u.national_id, u.role,
		       u.member_tier, u.is_verified, u.is_suspended, u.created_at, u.updated_at,
		       lo.last_login_at, COALESCE(oc.order_count, 0), COALESCE(le.history, '[]')
		FROM users u
		LEFT JOIN (
			SELECT user_id, MAX(created_at) AS last_login_at
			FROM login_events WHERE success = TRUE
			GROUP BY user_id
		) lo ON lo.user_id = u.id
		LEFT JOIN (
			SELECT user_id, COUNT(*) AS order_count
			FROM orders GROUP BY user_id
		) oc ON oc.user_id = u.id
		LEFT JOIN LATERAL (
			SELECT json_agg(ev) AS history FROM (
				SELECT id, ip_address, user_agent, success, created_at
				FROM login_events e WHERE e.user_id = u.id
				ORDER BY created_at DESC LIMIT 5
			) ev
		) le ON true
	` + where + `
		ORDER BY u.created_at DESC LIMIT ` + limitArg + ` OFFSET ` + offsetArg

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	users := make([]*domain.User, 0)
	for rows.Next() {
		var u domain.User
		var fullName, phone, nationalID *string
		var historyJSON []byte
		if err := rows.Scan(
			&u.ID, &u.Email, &u.PasswordHash, &fullName, &phone, &nationalID,
			&u.Role, &u.MemberTier, &u.IsVerified, &u.IsSuspended, &u.CreatedAt, &u.UpdatedAt,
			&u.LastLoginAt, &u.OrderCount, &historyJSON,
		); err != nil {
			return nil, 0, err
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
		u.LoginEvents = make([]domain.LoginEvent, 0)
		if len(historyJSON) > 0 {
			if err := json.Unmarshal(historyJSON, &u.LoginEvents); err != nil {
				return nil, 0, err
			}
		}
		users = append(users, &u)
	}
	return users, total, rows.Err()
}

func (r *userRepository) RecordLoginEvent(ctx context.Context, userID, ipAddress, userAgent string, success bool) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO login_events (user_id, ip_address, user_agent, success)
		VALUES ($1, $2, $3, $4)
	`, userID, ipAddress, userAgent, success)
	return err
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
