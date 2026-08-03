package domain

import "time"

type EventStatus string

const (
	EventUpcoming   EventStatus = "UPCOMING"
	EventPreWaiting EventStatus = "PRE_WAITING"
	EventLive       EventStatus = "LIVE"
	EventEnded      EventStatus = "ENDED"
	EventCancelled  EventStatus = "CANCELLED"
)

type Venue struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
	Capacity int    `json:"capacity"`
}

type Zone struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Type           string  `json:"type"` // SEATED | STANDING
	Price          float64 `json:"price"`
	TotalCapacity  int     `json:"total_capacity"`
	AvailableSeats int     `json:"available_seats"`
}

// EventSummary is the list-view shape returned by GET /api/v1/events.
type EventSummary struct {
	ID            string      `json:"id"`
	Title         string      `json:"title"`
	BannerURL     string      `json:"banner_url"`
	EventDate     time.Time   `json:"event_date"`
	Status        EventStatus `json:"status"`
	VenueName     string      `json:"venue_name"`
	VenueLocation string      `json:"venue_location"`
	MinPrice      float64     `json:"min_price"`
}

// EventDetail is the full shape returned by GET /api/v1/events/:id, joined with its venue
// and ticket zones.
type EventDetail struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	BannerURL   string `json:"banner_url"`
	// DescriptionRich is a JSON blob ({overview, lineup, schedule, rules, faq}) rendered as
	// tabs on the public event page — empty string means no rich content has been set.
	DescriptionRich        string      `json:"description_rich"`
	EventDate              time.Time   `json:"event_date"`
	Status                 EventStatus `json:"status"`
	RequiresIDVerification bool        `json:"requires_id_verification"`
	Venue                  Venue       `json:"venue"`
	Zones                  []Zone      `json:"zones"`
}

// AdminEventSummary is the list-view shape returned by GET /api/v1/admin/events. Unlike
// EventSummary, TicketsSold/Revenue are aggregated from paid orders (the real source of
// truth for sales), not the seed-time seat_zones.available_stock column.
type AdminEventSummary struct {
	ID            string      `json:"id"`
	Title         string      `json:"title"`
	BannerURL     string      `json:"banner_url"`
	EventDate     time.Time   `json:"event_date"`
	Status        EventStatus `json:"status"`
	VenueName     string      `json:"venue_name"`
	VenueLocation string      `json:"venue_location"`
	TotalCapacity int         `json:"total_capacity"`
	TicketsSold   int         `json:"tickets_sold"`
	Revenue       float64     `json:"revenue"`
}
