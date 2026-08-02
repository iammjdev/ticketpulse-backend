package domain

import "time"

type UserRole string

const (
	RoleUser      UserRole = "USER"
	RoleAdmin     UserRole = "ADMIN"
	RoleGateStaff UserRole = "GATE_STAFF"
)

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	FullName     string    `json:"full_name"`
	Phone        string    `json:"phone"`
	NationalID   string    `json:"national_id"`
	Role         UserRole  `json:"role"`
	MemberTier   string    `json:"member_tier"`
	IsVerified   bool      `json:"is_verified"`
	IsSuspended  bool      `json:"is_suspended"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	// LastLoginAt and OrderCount are populated only by ListUsers (the admin directory query) —
	// nil/zero on every other User-returning call (login, register, profile, etc).
	LastLoginAt *time.Time   `json:"last_login_at,omitempty"`
	OrderCount  int          `json:"order_count"`
	LoginEvents []LoginEvent `json:"login_events"`
}

// LoginEvent is one row of a user's login audit trail (login_events table), success or failure.
type LoginEvent struct {
	ID        string    `json:"id"`
	IPAddress string    `json:"ip_address"`
	UserAgent string    `json:"user_agent"`
	Success   bool      `json:"success"`
	CreatedAt time.Time `json:"created_at"`
}
