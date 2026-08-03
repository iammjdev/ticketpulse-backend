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
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"

	"github.com/iammjdev/ticketpulse-backend/internal/config"
	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/event"
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

	// 0. Load Environment Variables from .env file
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ No .env file found, reading from system environment variables")
	}

	// Helper function for ENV defaults
	getEnv := func(key, defaultValue string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		return defaultValue
	}

	// 1. Initialize PostgreSQL Connection Pool (pgxpool)
	dbHost := getEnv("DB_HOST", "127.0.0.1")
	dbPort := getEnv("DB_PORT", "5432")
	dbUser := getEnv("DB_USER", "postgres")
	dbPass := getEnv("DB_PASSWORD", "postgrespassword")
	dbName := getEnv("DB_NAME", "ticketpulse_db")
	dbSSL := getEnv("DB_SSLMODE", "disable")

	dbConnString := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		dbUser, dbPass, dbHost, dbPort, dbName, dbSSL)

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
	redisAddr := getEnv("REDIS_ADDR", "localhost:6379")
	redisPass := os.Getenv("REDIS_PASSWORD")
	redisClient := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPass,
	})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("❌ Redis Ping failed: %v\n", err)
	}
	log.Println("🔴 Redis Connection established successfully!")

	// 3. Initialize Redis Repository
	redisRepo, err := repository.NewRedisRepository(
		redisClient,
		"internal/repository/lua/reserve_ticket.lua",
		"internal/repository/lua/reserve_specific_seat.lua",
	)
	if err != nil {
		log.Fatalf("❌ Failed to initialize Redis Repository: %v\n", err)
	}

	orderRepo := repository.NewOrderRepository(dbPool)

	expirationWorker := worker.NewExpirationWorker(redisClient, redisRepo, orderRepo)
	go expirationWorker.Start(ctx)

	// 4. Initialize Kafka Producer & Worker Engine
	kafkaBroker := getEnv("KAFKA_BROKERS", "localhost:9092")
	kafkaBrokers := []string{kafkaBroker}
	kafkaTopic := "order.created"
	kafkaProducer := messaging.NewKafkaProducer(kafkaBrokers[0], kafkaTopic)
	defer kafkaProducer.Close()

	// Pre-warm Kafka Topic
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
	orderWorker := worker.NewOrderWorker(kafkaBrokers, kafkaTopic, "order-worker-group", dbPool, redisRepo)
	go orderWorker.Start(ctx)

	// 4b. Initialize Kafka Producer & Consumer Worker for Async E-Ticket Notifications.
	// Construction never fails even if Kafka is offline — kafka-go dials lazily on first
	// read/write, so publishing/consuming degrade gracefully instead of blocking startup.
	kafkaCfg := config.LoadKafkaConfig()
	notificationProducer := event.NewProducer(kafkaCfg.Brokers, kafkaCfg.OrderPaidTopic)
	defer notificationProducer.Close()

	// Pre-warm the ticketpulse.order.paid topic (same reasoning as the order.created pre-warm
	// above): without this, the NotificationWorker's consumer group can start before the topic
	// exists and miss the first real publish while Kafka lazily creates it.
	_ = notificationProducer.PublishOrderPaid(ctx, event.OrderPaidEvent{Timestamp: time.Now().UTC().Format(time.RFC3339)})

	smtpCfg := config.LoadSMTPConfig()
	if smtpCfg.Enabled {
		log.Printf("📧 SMTP configured (%s:%s) — e-ticket emails will be sent for real\n", smtpCfg.Host, smtpCfg.Port)
	} else {
		log.Println("📧 SMTP_HOST not set — e-ticket emails will be logged to stdout instead of sent")
	}

	// 5. Initialize Handlers
	queueHandler := handler.NewQueueHandler(redisRepo)

	userRepo := repository.NewUserRepository(dbPool)
	authService := service.NewAuthService(userRepo, redisClient)
	authHandler := handler.NewAuthHandler(authService)

	eventRepo := repository.NewEventRepository(dbPool)
	ticketHandler := handler.NewTicketHandler(redisRepo, redisClient, kafkaProducer, eventRepo, userRepo)
	eventHandler := handler.NewEventHandler(eventRepo, redisRepo)

	seatRepo := repository.NewSeatRepository(dbPool)
	seatHandler := handler.NewSeatHandler(seatRepo, redisRepo, kafkaProducer, eventRepo, userRepo)

	newsRepo := repository.NewNewsRepository(dbPool)
	newsService := service.NewNewsService(newsRepo, redisClient)
	newsHandler := handler.NewNewsHandler(newsService)

	userHandler := handler.NewUserHandler(userRepo)
	adminHandler := handler.NewAdminHandler(orderRepo, redisRepo, dbPool, kafkaCfg.Brokers[0], kafkaCfg.OrderPaidTopic, "notification-worker-group")
	adminQueueHandler := handler.NewAdminQueueHandler(redisRepo)

	webhookSecret := getEnv("PAYMENT_WEBHOOK_SECRET", "dev_webhook_secret_change_me")
	paymentService := service.NewPaymentService(orderRepo, redisRepo, userRepo, notificationProducer)
	paymentHandler := handler.NewPaymentHandler(paymentService, orderRepo, webhookSecret)
	orderHandler := handler.NewOrderHandler(orderRepo, userRepo, redisRepo, paymentService)

	notificationWorker := worker.NewNotificationWorker(
		kafkaCfg.Brokers, kafkaCfg.OrderPaidTopic, "notification-worker-group",
		orderRepo, eventRepo, userRepo, smtpCfg,
	)
	go notificationWorker.Start(ctx)

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

	app.Post("/api/v1/tickets/reserve", authmw.JWTMiddleware, ticketHandler.Reserve)
	app.Post("/api/v1/tickets/reserve-seat", authmw.JWTMiddleware, seatHandler.ReserveSeat)

	// Public Event & Venue Read Endpoints
	app.Get("/api/v1/events", eventHandler.ListEvents)
	app.Get("/api/v1/events/:id", eventHandler.GetEvent)
	app.Get("/api/v1/events/:id/seats", seatHandler.GetEventSeats)

	// Public News & Announcements Endpoints
	app.Get("/api/v1/news", newsHandler.ListNews)
	app.Get("/api/v1/news/:slug", newsHandler.GetNewsBySlug)

	// Virtual Queue Endpoints
	app.Post("/api/v1/queue/join", authmw.JWTMiddleware, queueHandler.JoinQueue)
	app.Get("/api/v1/queue/stream", queueHandler.StreamQueueStatus)

	// Auth Endpoints
	auth := app.Group("/api/v1/auth")
	auth.Post("/register", authHandler.Register)
	auth.Post("/verify-otp", authHandler.VerifyOTP)
	auth.Post("/resend-otp", authHandler.ResendOTP)
	auth.Post("/login", authHandler.Login)
	auth.Get("/me", authmw.JWTMiddleware, authHandler.Me)

	// User Profile Endpoints
	app.Put("/api/v1/users/profile", authmw.JWTMiddleware, authHandler.UpdateProfile)
	app.Put("/api/v1/users/password", authmw.JWTMiddleware, authHandler.ChangePassword)

	// Order History Endpoints
	app.Get("/api/v1/orders/my-orders", authmw.JWTMiddleware, orderHandler.GetMyOrders)
	app.Get("/api/v1/orders/:id/status", authmw.JWTMiddleware, paymentHandler.GetOrderStatus)
	app.Post("/api/v1/orders/:id/resend-email", authmw.JWTMiddleware, orderHandler.ResendEmail)

	// Payment Endpoints
	app.Post("/api/v1/payments/charge", authmw.JWTMiddleware, paymentHandler.CreateCharge)
	// Gateway-signed webhook — HMAC-verified inside the handler itself, not JWT-gated (the
	// caller is the payment gateway, not a logged-in user).
	app.Post("/api/v1/payments/webhook", paymentHandler.HandleWebhook)

	// Gate Verification Endpoint (ADMIN or GATE_STAFF)
	app.Post("/api/v1/tickets/verify", authmw.JWTMiddleware, authmw.RequireAnyRole(string(domain.RoleAdmin), string(domain.RoleGateStaff)), orderHandler.VerifyTicket)

	// Admin Endpoints (JWT + ADMIN role required)
	admin := app.Group("/api/v1/admin", authmw.JWTMiddleware, authmw.RequireRole(string(domain.RoleAdmin)))
	admin.Get("/ping", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok", "scope": "admin"})
	})
	admin.Post("/events", eventHandler.CreateEvent)
	admin.Get("/events", eventHandler.AdminListEvents)
	admin.Get("/venues", eventHandler.AdminListVenues)
	admin.Put("/events/:id", eventHandler.AdminUpdateEventMetadata)
	admin.Delete("/events/:id", eventHandler.AdminDeleteEvent)
	admin.Put("/events/:id/zones", eventHandler.UpdateZones)
	admin.Post("/events/:id/status", eventHandler.UpdateEventStatus)
	admin.Post("/events/:id/seats", seatHandler.AdminBulkCreateSeats)
	admin.Post("/events/:id/seats/ai-preview", seatHandler.AdminAIPreviewSeats)
	admin.Post("/events/:id/seats/ai-confirm", seatHandler.AdminAIConfirmSeats)

	admin.Get("/news", newsHandler.AdminListNews)
	admin.Post("/news", newsHandler.CreateNews)
	admin.Put("/news/:id", newsHandler.UpdateNews)
	admin.Delete("/news/:id", newsHandler.DeleteNews)

	admin.Get("/users", userHandler.AdminListUsers)
	admin.Post("/users", userHandler.AdminCreateUser)
	admin.Put("/users/:id", userHandler.AdminEditUser)
	admin.Patch("/users/:id/role", userHandler.AdminUpdateUser)
	admin.Delete("/users/:id", userHandler.AdminDeleteUser)

	admin.Get("/orders", orderHandler.AdminListOrders)
	admin.Post("/orders/:id/resend-email", orderHandler.ResendEmail)
	admin.Post("/orders/:id/cancel", orderHandler.AdminCancelOrder)

	admin.Get("/stats", adminHandler.Stats)
	admin.Get("/dashboard/sales-series", adminHandler.SalesSeries)
	admin.Get("/dashboard/zone-breakdown", adminHandler.ZoneBreakdown)
	admin.Get("/dashboard/health", adminHandler.Health)

	admin.Get("/queue/status", adminQueueHandler.Status)
	admin.Put("/queue/rate", adminQueueHandler.SetRate)
	admin.Put("/queue/pause", adminQueueHandler.SetPaused)
	admin.Post("/queue/flush", adminQueueHandler.Flush)

	// Dev tool: applies the exact success side effects of a real gateway webhook without
	// needing PAYMENT_WEBHOOK_SECRET on the frontend. See /admin/payment-simulator.
	admin.Post("/payments/simulate/:orderId", paymentHandler.SimulatePayment)

	// Graceful Shutdown Setup
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	serverPort := getEnv("PORT", "8080")
	go func() {
		log.Printf("🚀 TicketPulse API Server starting on :%s...\n", serverPort)
		if err := app.Listen(":" + serverPort); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ Server error: %v\n", err)
		}
	}()

	<-sigChan
	log.Println("🛑 Shutting down TicketPulse API...")
	cancel()
	_ = app.Shutdown()
	log.Println("✅ Server stopped successfully.")
}
