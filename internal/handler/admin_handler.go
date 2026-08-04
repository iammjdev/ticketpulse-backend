package handler

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iammjdev/ticketpulse-backend/internal/repository"
	"github.com/iammjdev/ticketpulse-backend/pkg/messaging"
)

type AdminHandler struct {
	orders     repository.OrderRepository
	redis      repository.RedisRepository
	dbPool     *pgxpool.Pool
	kafkaAddr  string
	kafkaTopic string
	kafkaGroup string
}

func NewAdminHandler(orders repository.OrderRepository, redis repository.RedisRepository, dbPool *pgxpool.Pool, kafkaAddr, kafkaTopic, kafkaGroup string) *AdminHandler {
	return &AdminHandler{
		orders:     orders,
		redis:      redis,
		dbPool:     dbPool,
		kafkaAddr:  kafkaAddr,
		kafkaTopic: kafkaTopic,
		kafkaGroup: kafkaGroup,
	}
}

// Stats aggregates platform-wide gross revenue, tickets sold, and gate scan velocity from
// PostgreSQL (paid orders) alongside the total active virtual-queue length and payment-hold
// count from Redis. ADMIN only.
func (h *AdminHandler) Stats(c *fiber.Ctx) error {
	revenue, ticketsSold, checkedIn, err := h.orders.AdminStats(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to compute stats"})
	}

	queueLength, err := h.redis.TotalQueueLength(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to compute queue length"})
	}

	activeHolds, err := h.redis.GetActiveHoldCount(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to compute active holds"})
	}

	scanVelocity, err := h.orders.GateScanVelocity(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to compute gate scan velocity"})
	}

	checkedInShare := 0.0
	if ticketsSold > 0 {
		checkedInShare = float64(checkedIn) / float64(ticketsSold)
	}

	return c.JSON(fiber.Map{
		"gross_revenue":       revenue,
		"tickets_sold":        ticketsSold,
		"checked_in_count":    checkedIn,
		"checked_in_share":    checkedInShare,
		"active_queue_length": queueLength,
		"active_holds":        activeHolds,
		"gate_scan_velocity":  scanVelocity,
	})
}

// rangeWindow maps a dashboard range key to a (since, granularity) pair for the current window,
// plus the matching prior window of equal duration for period-over-period deltas.
func rangeWindow(rangeKey string) (since time.Time, granularity string, priorSince, priorUntil time.Time, ok bool) {
	now := time.Now()
	switch rangeKey {
	case "today":
		since = now.Add(-24 * time.Hour)
		granularity = "hour"
		priorSince = since.Add(-24 * time.Hour)
		priorUntil = since
	case "7d":
		since = now.AddDate(0, 0, -7)
		granularity = "day"
		priorSince = since.AddDate(0, 0, -7)
		priorUntil = since
	case "30d":
		since = now.AddDate(0, 0, -30)
		granularity = "day"
		priorSince = since.AddDate(0, 0, -30)
		priorUntil = since
	case "ytd":
		since = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
		granularity = "month"
		priorSince = since.AddDate(-1, 0, 0)
		priorUntil = since
	default:
		return time.Time{}, "", time.Time{}, time.Time{}, false
	}
	return since, granularity, priorSince, priorUntil, true
}

// SalesSeries returns bucketed revenue/tickets for the requested range plus the prior
// equal-length window's totals, so the frontend can render the trend chart and compute a
// period-over-period delta without another round trip. ADMIN only.
func (h *AdminHandler) SalesSeries(c *fiber.Ctx) error {
	rangeKey := c.Query("range", "7d")
	since, granularity, priorSince, priorUntil, ok := rangeWindow(rangeKey)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "range must be one of today, 7d, 30d, ytd"})
	}

	points, err := h.orders.SalesSeries(c.Context(), since, granularity)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to compute sales series"})
	}

	currentRevenue, currentTickets, err := h.orders.RangeTotals(c.Context(), since, time.Now())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to compute range totals"})
	}

	priorRevenue, priorTickets, err := h.orders.RangeTotals(c.Context(), priorSince, priorUntil)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to compute prior range totals"})
	}

	return c.JSON(fiber.Map{
		"range":  rangeKey,
		"points": points,
		"current_total": fiber.Map{
			"revenue":      currentRevenue,
			"tickets_sold": currentTickets,
		},
		"prior_total": fiber.Map{
			"revenue":      priorRevenue,
			"tickets_sold": priorTickets,
		},
	})
}

// ZoneBreakdown returns sold/capacity/revenue aggregated per zone name across every event, for
// the admin dashboard's zone occupancy & revenue share widget. ADMIN only.
func (h *AdminHandler) ZoneBreakdown(c *fiber.Ctx) error {
	zones, err := h.orders.ZoneBreakdown(c.Context(), c.Query("event_id"))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to compute zone breakdown"})
	}
	return c.JSON(fiber.Map{"zones": zones})
}

// FlushCache deletes every cache:* key (a static read-through cache namespace) and reports
// how many keys were cleared. Strictly scoped — seat locks, virtual queues, ticket/payment
// holds, and session state live under other prefixes and are never touched by this action.
// ADMIN only, irreversible.
func (h *AdminHandler) FlushCache(c *fiber.Ctx) error {
	cleared, err := h.redis.FlushCacheKeys(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to flush cache"})
	}
	return c.JSON(fiber.Map{"cleared_count": cleared})
}

// Health reports live infrastructure metrics — Redis payment-hold count, Kafka consumer lag on
// the order-paid topic, and PostgreSQL connection pool stats + ping latency — so the admin
// dashboard's system health panel reflects the real running stack instead of a placeholder.
// Each subsystem degrades independently: an unreachable Kafka broker doesn't fail the whole
// response, it's reported as a degraded status. ADMIN only.
func (h *AdminHandler) Health(c *fiber.Ctx) error {
	ctx := c.Context()

	activeHolds, err := h.redis.GetActiveHoldCount(ctx)
	redisStatus := "healthy"
	if err != nil {
		redisStatus = "degraded"
	}
	memoryBytes, memErr := h.redis.MemoryUsageBytes(ctx)
	if memErr != nil {
		redisStatus = "degraded"
	}

	kafkaCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	kafkaLag, kafkaErr := messaging.ConsumerLag(kafkaCtx, h.kafkaAddr, h.kafkaTopic, h.kafkaGroup)
	kafkaStatus := "healthy"
	if kafkaErr != nil {
		kafkaStatus = "degraded"
	}

	pingStart := time.Now()
	pgErr := h.dbPool.Ping(ctx)
	pingMs := float64(time.Since(pingStart).Microseconds()) / 1000.0
	pgStatus := "healthy"
	if pgErr != nil {
		pgStatus = "degraded"
	}
	stat := h.dbPool.Stat()

	return c.JSON(fiber.Map{
		"redis": fiber.Map{
			"status":            redisStatus,
			"active_holds":      activeHolds,
			"memory_used_bytes": memoryBytes,
		},
		"kafka": fiber.Map{
			"status":         kafkaStatus,
			"consumer_lag":   kafkaLag,
			"topic":          h.kafkaTopic,
			"consumer_group": h.kafkaGroup,
		},
		"postgres": fiber.Map{
			"status":         pgStatus,
			"total_conns":    stat.TotalConns(),
			"idle_conns":     stat.IdleConns(),
			"acquired_conns": stat.AcquiredConns(),
			"max_conns":      stat.MaxConns(),
			"ping_ms":        pingMs,
		},
	})
}
