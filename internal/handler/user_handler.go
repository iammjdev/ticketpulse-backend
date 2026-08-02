package handler

import (
	"errors"
	"net/mail"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/crypto/bcrypt"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
)

type UserHandler struct {
	users repository.UserRepository
}

func NewUserHandler(users repository.UserRepository) *UserHandler {
	return &UserHandler{users: users}
}

func adminUserResponse(u *domain.User) fiber.Map {
	loginEvents := u.LoginEvents
	if loginEvents == nil {
		loginEvents = []domain.LoginEvent{}
	}
	return fiber.Map{
		"id":            u.ID,
		"email":         u.Email,
		"full_name":     u.FullName,
		"phone":         u.Phone,
		"role":          u.Role,
		"member_tier":   u.MemberTier,
		"is_verified":   u.IsVerified,
		"is_suspended":  u.IsSuspended,
		"created_at":    u.CreatedAt,
		"updated_at":    u.UpdatedAt,
		"last_login_at": u.LastLoginAt,
		"order_count":   u.OrderCount,
		"login_events":  loginEvents,
	}
}

func adminUserListResponse(users []*domain.User, total, page, limit int) fiber.Map {
	items := make([]fiber.Map, 0, len(users))
	for _, u := range users {
		items = append(items, adminUserResponse(u))
	}
	totalPages := 0
	if limit > 0 {
		totalPages = (total + limit - 1) / limit
	}
	return fiber.Map{
		"users":       items,
		"total":       total,
		"page":        page,
		"limit":       limit,
		"total_pages": totalPages,
	}
}

// resolveRole normalizes an incoming role string to a valid domain.UserRole. "STAFF_SCANNER"
// is accepted as a frontend-facing alias for the actual GATE_STAFF enum value stored in
// Postgres — both spellings mean the same role, the gate ticket-scanning staff.
func resolveRole(raw string) (domain.UserRole, bool) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case string(domain.RoleUser):
		return domain.RoleUser, true
	case string(domain.RoleAdmin):
		return domain.RoleAdmin, true
	case string(domain.RoleGateStaff), "STAFF_SCANNER":
		return domain.RoleGateStaff, true
	default:
		return "", false
	}
}

// AdminListUsers returns the user directory with optional role/search filters, paginated,
// newest accounts first. ADMIN only.
func (h *UserHandler) AdminListUsers(c *fiber.Ctx) error {
	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("limit", "20"))

	filter := repository.UserListFilter{
		Search: strings.TrimSpace(c.Query("search")),
		Page:   page,
		Limit:  limit,
	}
	if roleParam := strings.TrimSpace(c.Query("role")); roleParam != "" {
		role, ok := resolveRole(roleParam)
		if !ok {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid role filter"})
		}
		filter.Role = string(role)
	}

	users, total, err := h.users.ListUsers(c.Context(), filter)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch users"})
	}

	appliedPage := max(page, 1)
	appliedLimit := limit
	if appliedLimit < 1 || appliedLimit > 100 {
		appliedLimit = 20
	}
	return c.JSON(adminUserListResponse(users, total, appliedPage, appliedLimit))
}

type updateUserRoleRequest struct {
	Role   *string `json:"role"`
	Status *string `json:"status"`
}

// AdminUpdateUser patches a user's role and/or ACTIVE/SUSPENDED status — at least one of the
// two fields is required. ADMIN only.
func (h *UserHandler) AdminUpdateUser(c *fiber.Ctx) error {
	id := c.Params("id")

	var req updateUserRoleRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}
	if req.Role == nil && req.Status == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "role or status is required"})
	}

	var user *domain.User
	var err error

	if req.Role != nil {
		role, ok := resolveRole(*req.Role)
		if !ok {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "role must be one of USER, ADMIN, STAFF_SCANNER"})
		}
		user, err = h.users.UpdateRole(c.Context(), id, role)
		if err != nil {
			if errors.Is(err, repository.ErrUserNotFound) {
				return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User not found"})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update role"})
		}
	}

	if req.Status != nil {
		status := strings.ToUpper(strings.TrimSpace(*req.Status))
		if status != "ACTIVE" && status != "SUSPENDED" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "status must be ACTIVE or SUSPENDED"})
		}
		user, err = h.users.UpdateSuspended(c.Context(), id, status == "SUSPENDED")
		if err != nil {
			if errors.Is(err, repository.ErrUserNotFound) {
				return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User not found"})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update status"})
		}
	}

	return c.JSON(fiber.Map{"user": adminUserResponse(user)})
}

type createUserRequest struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// AdminCreateUser creates a new staff/admin account directly — pre-verified, no OTP flow,
// since the requesting admin is vouching for the address. ADMIN only.
func (h *UserHandler) AdminCreateUser(c *fiber.Ctx) error {
	var req createUserRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	if _, err := mail.ParseAddress(email); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "A valid email is required"})
	}
	if len(req.Password) < 8 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Password must be at least 8 characters"})
	}
	role, ok := resolveRole(req.Role)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "role must be one of USER, ADMIN, STAFF_SCANNER"})
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 10)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to hash password"})
	}

	user, err := h.users.AdminCreateUser(c.Context(), strings.TrimSpace(req.FullName), email, string(hash), role)
	if err != nil {
		if errors.Is(err, repository.ErrEmailTaken) {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "An account with this email already exists"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create user"})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"user": adminUserResponse(user)})
}

type editUserRequest struct {
	FullName    string `json:"full_name"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	IsSuspended bool   `json:"is_suspended"`
}

// AdminEditUser overwrites a user's full profile — name, email, role, and suspended state —
// in one call, for the admin directory's full edit drawer. ADMIN only.
func (h *UserHandler) AdminEditUser(c *fiber.Ctx) error {
	id := c.Params("id")

	var req editUserRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	if _, err := mail.ParseAddress(email); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "A valid email is required"})
	}
	role, ok := resolveRole(req.Role)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "role must be one of USER, ADMIN, STAFF_SCANNER"})
	}

	user, err := h.users.AdminUpdateUser(c.Context(), id, strings.TrimSpace(req.FullName), email, role, req.IsSuspended)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrUserNotFound):
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User not found"})
		case errors.Is(err, repository.ErrEmailTaken):
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "An account with this email already exists"})
		default:
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update user"})
		}
	}

	return c.JSON(fiber.Map{"user": adminUserResponse(user)})
}

// AdminDeleteUser soft-deletes a user (sets deleted_at) — order and login history are kept
// for audit purposes, the account just stops being able to log in or appear in the directory.
// ADMIN only.
func (h *UserHandler) AdminDeleteUser(c *fiber.Ctx) error {
	id := c.Params("id")
	if err := h.users.SoftDeleteUser(c.Context(), id); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to delete user"})
	}
	return c.JSON(fiber.Map{"message": "User deleted", "id": id})
}
