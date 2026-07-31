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
