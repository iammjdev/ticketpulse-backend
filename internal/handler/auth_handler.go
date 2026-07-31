package handler

import (
	"errors"
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
	"github.com/iammjdev/ticketpulse-backend/internal/service"
)

var (
	emailPattern      = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	nationalIDPattern = regexp.MustCompile(`^\d{13}$`)
	nonDigitPattern   = regexp.MustCompile(`\D`)
)

type AuthHandler struct {
	authService *service.AuthService
}

func NewAuthHandler(authService *service.AuthService) *AuthHandler {
	return &AuthHandler{authService: authService}
}

func userResponse(u *domain.User) fiber.Map {
	return fiber.Map{
		"id":          u.ID,
		"email":       u.Email,
		"full_name":   u.FullName,
		"phone":       u.Phone,
		"national_id": u.NationalID,
		"role":        u.Role,
		"member_tier": u.MemberTier,
		"is_verified": u.IsVerified,
	}
}

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Register creates (or re-issues an OTP for) an unverified account and emails a 6-digit code.
// No session token is returned here — one is only issued once VerifyOTP succeeds.
func (h *AuthHandler) Register(c *fiber.Ctx) error {
	var req registerRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	if !emailPattern.MatchString(req.Email) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "A valid email address is required"})
	}
	if len(req.Password) < 6 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Password must be at least 6 characters"})
	}

	if err := h.authService.Register(c.Context(), req.Email, req.Password); err != nil {
		if errors.Is(err, service.ErrEmailTaken) {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": err.Error()})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to register user"})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"message": "OTP_SENT", "email": req.Email})
}

type verifyOTPRequest struct {
	Email string `json:"email"`
	OTP   string `json:"otp"`
}

// VerifyOTP confirms the 6-digit code sent to the user's email, flips is_verified, and
// issues a 24h session token.
func (h *AuthHandler) VerifyOTP(c *fiber.Ctx) error {
	var req verifyOTPRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.OTP = strings.TrimSpace(req.OTP)

	user, token, err := h.authService.VerifyOTP(c.Context(), req.Email, req.OTP)
	if err != nil {
		if errors.Is(err, service.ErrInvalidOrExpiredOTP) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "INVALID_OR_EXPIRED_OTP"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to verify code"})
	}

	return c.JSON(fiber.Map{"token": token, "user": userResponse(user)})
}

type resendOTPRequest struct {
	Email string `json:"email"`
}

// ResendOTP re-issues a code, gated by a 60s per-email cooldown.
func (h *AuthHandler) ResendOTP(c *fiber.Ctx) error {
	var req resendOTPRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	if err := h.authService.ResendOTP(c.Context(), req.Email); err != nil {
		switch {
		case errors.Is(err, service.ErrResendCooldown):
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": "PLEASE_WAIT_BEFORE_RESEND"})
		case errors.Is(err, repository.ErrUserNotFound):
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "USER_NOT_FOUND"})
		case errors.Is(err, service.ErrEmailTaken):
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "ALREADY_VERIFIED"})
		default:
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to resend code"})
		}
	}

	return c.JSON(fiber.Map{"message": "OTP_SENT", "email": req.Email})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *AuthHandler) Login(c *fiber.Ctx) error {
	var req loginRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	user, token, err := h.authService.Login(c.Context(), strings.TrimSpace(strings.ToLower(req.Email)), req.Password)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidCredentials):
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": err.Error()})
		case errors.Is(err, service.ErrEmailNotVerified):
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "EMAIL_NOT_VERIFIED"})
		default:
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to login"})
		}
	}

	return c.JSON(fiber.Map{"token": token, "user": userResponse(user)})
}

type updateProfileRequest struct {
	FullName   string `json:"full_name"`
	Phone      string `json:"phone"`
	NationalID string `json:"national_id"`
}

// UpdateProfile lets an authenticated user update their name, phone, and national ID
// (the latter unlocking reservation of events that require identity verification).
func (h *AuthHandler) UpdateProfile(c *fiber.Ctx) error {
	userID, _ := c.Locals("userId").(string)

	var req updateProfileRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	req.FullName = strings.TrimSpace(req.FullName)
	req.NationalID = nonDigitPattern.ReplaceAllString(req.NationalID, "")
	if req.FullName == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Full name is required"})
	}
	if req.NationalID != "" && !nationalIDPattern.MatchString(req.NationalID) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "National ID must be 13 numeric digits"})
	}

	user, err := h.authService.UpdateProfile(c.Context(), userID, req.FullName, req.Phone, req.NationalID)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update profile"})
	}

	return c.JSON(fiber.Map{"user": userResponse(user)})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword verifies the caller's current password before committing a new one.
func (h *AuthHandler) ChangePassword(c *fiber.Ctx) error {
	userID, _ := c.Locals("userId").(string)

	var req changePasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}
	if len(req.NewPassword) < 6 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "New password must be at least 6 characters"})
	}

	if err := h.authService.ChangePassword(c.Context(), userID, req.CurrentPassword, req.NewPassword); err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidCurrentPassword):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "INVALID_CURRENT_PASSWORD"})
		case errors.Is(err, repository.ErrUserNotFound):
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User not found"})
		default:
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update password"})
		}
	}

	return c.JSON(fiber.Map{"message": "PASSWORD_UPDATED_SUCCESSFULLY"})
}

func (h *AuthHandler) Me(c *fiber.Ctx) error {
	userID, _ := c.Locals("userId").(string)

	user, err := h.authService.GetProfile(c.Context(), userID)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch profile"})
	}

	return c.JSON(fiber.Map{"user": userResponse(user)})
}
