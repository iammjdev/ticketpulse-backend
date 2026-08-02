package handler

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

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
