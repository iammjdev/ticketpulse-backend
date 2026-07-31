package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/iammjdev/ticketpulse-backend/internal/service"
)

// JWTMiddleware extracts and validates the Bearer token, then stores the
// authenticated user's id and role in the request locals.
func JWTMiddleware(c *fiber.Ctx) error {
	header := c.Get("Authorization")
	if header == "" || !strings.HasPrefix(header, "Bearer ") {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Missing or invalid Authorization header"})
	}

	tokenString := strings.TrimPrefix(header, "Bearer ")
	claims, err := service.ParseToken(tokenString)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Invalid or expired token"})
	}

	c.Locals("userId", claims.Subject)
	c.Locals("userRole", string(claims.Role))
	return c.Next()
}

// RequireRole guards a route to only the given role. Must run after JWTMiddleware.
func RequireRole(requiredRole string) fiber.Handler {
	return RequireAnyRole(requiredRole)
}

// RequireAnyRole guards a route to any of the given roles. Must run after JWTMiddleware.
func RequireAnyRole(roles ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		role, _ := c.Locals("userRole").(string)
		for _, r := range roles {
			if role == r {
				return c.Next()
			}
		}
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Insufficient permissions"})
	}
}
