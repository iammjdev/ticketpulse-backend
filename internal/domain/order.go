package domain

import "time"

type OrderStatus string

const (
	OrderPending   OrderStatus = "PENDING"
	OrderCompleted OrderStatus = "COMPLETED"
	OrderCheckedIn OrderStatus = "CHECKED_IN"
	OrderCancelled OrderStatus = "CANCELLED"
	OrderExpired   OrderStatus = "EXPIRED"
)

type Order struct {
	ID          string
	UserID      string
	EventID     string
	EventTitle  string
	VenueName   string
	EventDate   time.Time
	PosterURL   string
	ZoneID      string
	Quantity    int
	TotalAmount float64
	Status      OrderStatus
	CreatedAt   time.Time
	CheckedInAt *time.Time
}

// AdminOrderSummary is the list-view shape returned by GET /api/v1/admin/orders, joined with
// the placing user and the event so the admin table needs no extra round trips.
type AdminOrderSummary struct {
	ID           string      `json:"id"`
	UserID       string      `json:"user_id"`
	UserEmail    string      `json:"user_email"`
	UserFullName string      `json:"user_full_name"`
	EventID      string      `json:"event_id"`
	EventTitle   string      `json:"event_title"`
	ZoneID       string      `json:"zone_id"`
	Quantity     int         `json:"quantity"`
	TotalAmount  float64     `json:"total_amount"`
	Status       OrderStatus `json:"status"`
	CreatedAt    time.Time   `json:"created_at"`
	CheckedInAt  *time.Time  `json:"checked_in_at"`
}
