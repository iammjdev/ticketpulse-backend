package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/handler"
	authmw "github.com/iammjdev/ticketpulse-backend/internal/middleware"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
	"github.com/iammjdev/ticketpulse-backend/internal/service"
	"github.com/iammjdev/ticketpulse-backend/internal/worker"
	"github.com/iammjdev/ticketpulse-backend/pkg/messaging"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Initialize PostgreSQL Connection Pool (pgxpool)
	dbConnString := "postgres://postgres:postgrespassword@localhost:5432/ticketpulse_db?sslmode=disable"
	dbPool, err := pgxpool.New(ctx, dbConnString)
	if err != nil {
		log.Fatalf("❌ Unable to connect to PostgreSQL pool: %v\n", err)
	}
	defer dbPool.Close()

	if err := dbPool.Ping(ctx); err != nil {
		log.Fatalf("❌ PostgreSQL Ping failed: %v\n", err)
	}
	log.Println("🐘 PostgreSQL Connection Pool established successfully!")

	// 2. Initialize Redis Client
	redisClient := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("❌ Redis Ping failed: %v\n", err)
	}

	// 3. Initialize Redis Repository
	redisRepo, err := repository.NewRedisRepository(redisClient, "internal/repository/lua/reserve_ticket.lua")
	if err != nil {
		log.Fatalf("❌ Failed to initialize Redis Repository: %v\n", err)
	}

	expirationWorker := worker.NewExpirationWorker(redisClient, redisRepo)
	go expirationWorker.Start(ctx)

	// 4. Initialize Kafka Producer & Worker Engine
	kafkaBrokers := []string{"localhost:9092"}
	kafkaTopic := "order.created"
	kafkaProducer := messaging.NewKafkaProducer(kafkaBrokers[0], kafkaTopic)
	defer kafkaProducer.Close()

	// Pre-warm Kafka Topic (Publish dummy event to ensure topic existence)
	_ = kafkaProducer.PublishOrderCreated(ctx, messaging.OrderCreatedEvent{
		OrderID:   uuid.New().String(),
		EventID:   "11111111-1111-1111-1111-111111111111",
		ZoneID:    "22222222-2222-2222-2222-222222222222",
		UserID:    "33333333-3333-3333-3333-333333333333",
		Quantity:  0,
		Price:     0.0,
		Timestamp: time.Now(),
	})

	// Initialize & Run Order Processing Worker in Background Goroutine
	orderWorker := worker.NewOrderWorker(kafkaBrokers, kafkaTopic, "order-worker-group", dbPool)
	go orderWorker.Start(ctx)

	// 5. Initialize Handlers
	queueHandler := handler.NewQueueHandler(redisRepo)

	userRepo := repository.NewUserRepository(dbPool)
	authService := service.NewAuthService(userRepo)
	authHandler := handler.NewAuthHandler(authService)

	// 6. Initialize Fiber App Engine
	app := fiber.New(fiber.Config{
		AppName: "TicketPulse Enterprise Engine v1.0",
	})

	app.Use(cors.New(cors.Config{
		AllowOrigins:     "http://localhost:3000",
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization",
		AllowMethods:     "GET, POST, HEAD, PUT, DELETE, PATCH, OPTIONS",
		AllowCredentials: true,
	}))

	// Health Check Endpoint
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"status": "online",
			"services": fiber.Map{
				"postgres": "ok",
				"redis":    "ok",
				"kafka":    "ok",
			},
		})
	})

	// Ticket Stock Endpoints
	app.Post("/api/v1/tickets/warmup", func(c *fiber.Ctx) error {
		type WarmupReq struct {
			EventID string `json:"event_id"`
			ZoneID  string `json:"zone_id"`
			Stock   int    `json:"stock"`
		}
		var req WarmupReq
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
		}
		if err := redisRepo.WarmupStock(c.Context(), req.EventID, req.ZoneID, req.Stock); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"message": "Stock warmed up successfully in Redis!", "event_id": req.EventID, "zone_id": req.ZoneID, "stock": req.Stock})
	})

	app.Post("/api/v1/tickets/reserve", authmw.JWTMiddleware, func(c *fiber.Ctx) error {
		type ReserveReq struct {
			EventID  string  `json:"event_id"`
			ZoneID   string  `json:"zone_id"`
			UserID   string  `json:"user_id"`
			Quantity int     `json:"quantity"`
			Price    float64 `json:"price"`
		}
		var req ReserveReq
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
		}

		// Authenticated user id from JWT always takes precedence over the request body
		if userID, ok := c.Locals("userId").(string); ok && userID != "" {
			req.UserID = userID
		}

		// Provide default fallback values for testing payload
		if req.ZoneID == "" {
			req.ZoneID = "22222222-2222-2222-2222-222222222222"
		}
		if req.Price == 0 {
			req.Price = 1500.00
		}

		// Atomic Inventory Deduction in Redis Lua
		status, err := redisRepo.ReserveTicket(c.Context(), req.EventID, req.ZoneID, req.Quantity)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		if status != "RESERVED" {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"status": status, "message": "Sorry, tickets for this zone are sold out!"})
		}

		// Dequeue user from Redis Virtual Waiting Room after successful reservation
		if err := redisRepo.DequeueUser(c.Context(), req.EventID, req.UserID); err != nil {
			log.Printf("⚠️ Failed to dequeue user %s from Redis: %v\n", req.UserID, err)
		} else {
			log.Printf("🗑️ Successfully dequeued user %s from Redis queue\n", req.UserID)
		}

		// Publish Event to Kafka for Asynchronous Processing
		orderID := uuid.New().String()
		event := messaging.OrderCreatedEvent{
			OrderID:   orderID,
			EventID:   req.EventID,
			ZoneID:    req.ZoneID,
			UserID:    req.UserID,
			Quantity:  req.Quantity,
			Price:     req.Price,
			Timestamp: time.Now(),
		}

		if err := kafkaProducer.PublishOrderCreated(c.Context(), event); err != nil {
			log.Printf("⚠️ Failed to publish order event to Kafka: %v\n", err)
		} else {
			log.Printf("📢 Published OrderCreatedEvent to Kafka: OrderID=%s\n", orderID)
		}

		holdKey := fmt.Sprintf("hold:%s:%s:%d:%s", req.EventID, req.ZoneID, req.Quantity, req.UserID)
		_ = redisClient.Set(c.Context(), holdKey, "HELD", 10*time.Minute).Err()

		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"status":   "RESERVED",
			"message":  "Ticket reserved! Order is being processed asynchronously.",
			"order_id": orderID,
		})
	})

	// Virtual Queue Endpoints
	app.Post("/api/v1/queue/join", authmw.JWTMiddleware, queueHandler.JoinQueue)
	app.Get("/api/v1/queue/stream", queueHandler.StreamQueueStatus)

	// Auth Endpoints
	auth := app.Group("/api/v1/auth")
	auth.Post("/register", authHandler.Register)
	auth.Post("/login", authHandler.Login)
	auth.Get("/me", authmw.JWTMiddleware, authHandler.Me)

	// Admin Endpoints (JWT + ADMIN role required)
	admin := app.Group("/api/v1/admin", authmw.JWTMiddleware, authmw.RequireRole(string(domain.RoleAdmin)))
	admin.Get("/ping", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok", "scope": "admin"})
	})

	// Graceful Shutdown Setup
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Println("🚀 TicketPulse API Server starting on :8080...")
		if err := app.Listen(":8080"); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ Server error: %v\n", err)
		}
	}()

	<-sigChan
	log.Println("🛑 Shutting down TicketPulse API...")
	cancel()
	_ = app.Shutdown()
	log.Println("✅ Server stopped successfully.")
}
