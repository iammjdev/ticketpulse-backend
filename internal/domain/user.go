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
}
