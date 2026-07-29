package main

import (
	"context"
	"log"
	"net/http"

	"github.com/iammjdev/ticketpulse-backend/internal/repository"
)

func main() {
	app := fiber.New(fiber.Config{
		AppName: "TicketPulse API Engine v1.0",
	})

	ctx := context.Background()

	// Connect Redis
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	// Connect PostgreSQL (Future usage)
	_, err := pgx.Connect(ctx, "postgres://postgres:postgrespassword@localhost:5432/ticketpulse_db?sslmode=disable")
	if err != nil {
		log.Printf("⚠️ PostgreSQL Warning: %v", err)
	}

	// Initialize Redis Repository & Load Lua Script
	redisRepo, err := repository.NewRedisRepository(rdb, "internal/repository/lua/reserve_ticket.lua")
	if err != nil {
		log.Fatalf("Failed to init Redis Lua Engine: %v", err)
	}
	log.Println("⚡ Redis Lua Engine loaded successfully!")

	// Endpoint: Warm-up Stock on
	app.Post("/api/v1/tickets/warmup", func(c *fiber.Ctx) error {
		type WarmupRequest struct {
			EventID string `json:"event_id"`
			ZoneID  string `json:"zone_id"`
			Stock   int    `json:"stock"`
		}

		var req WarmupRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid payload"})
		}

		if err := redisRepo.WarmupStock(c.Context(), req.EventID, req.ZoneID, req.Stock); err != nil {
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}

		return c.JSON(fiber.Map{
			"message":  "Stock warmed up successfully in Redis!",
			"event_id": req.EventID,
			"zone_id":  req.ZoneID,
			"stock":    req.Stock,
		})
	})

	// Endpoint: Reserve Ticket (Cut stock via Atomic Lua Script)
	app.Post("/api/v1/tickets/reserve", func(c *fiber.Ctx) error {
		type ReserveRequest struct {
			EventID  string `json:"event_id"`
			ZoneID   string `json:"zone_id"`
			Quantity int    `json:"quantity"`
		}

		var req ReserveRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid payload"})
		}

		status, err := redisRepo.ReserveTicketAtomic(c.Context(), req.EventID, req.ZoneID, req.Quantity)
		if err != nil {
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}

		switch status {
		case 1:
			return c.Status(http.StatusOK).JSON(fiber.Map{
				"status":  "RESERVED",
				"message": "Ticket reserved successfully!",
			})
		case 0:
			return c.Status(http.StatusConflict).JSON(fiber.Map{
				"status":  "SOLD_OUT",
				"message": "Sorry, tickets for this zone are sold out!",
			})
		default:
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{
				"status":  "NOT_WARMED_UP",
				"message": "Stock has not been initialized for this event zone.",
			})
		}
	})

	log.Println("Server starting on :8080...")
	log.Fatal(app.Listen(":8080"))
}
