package domain

import "time"

// SalesSeriesPoint is one bucket (hour/day/month) of aggregated revenue and ticket volume for
// the admin dashboard's sales & ticket velocity chart.
type SalesSeriesPoint struct {
	Bucket      time.Time `json:"bucket"`
	Revenue     float64   `json:"revenue"`
	TicketsSold int       `json:"tickets_sold"`
}

// ZoneBreakdownPoint aggregates sold tickets, capacity, and revenue across every seat_zones row
// sharing the same zone_name (e.g. every event's "VIP" zone counted together), for the admin
// dashboard's zone occupancy & revenue share widget.
type ZoneBreakdownPoint struct {
	Name     string  `json:"name"`
	Sold     int     `json:"sold"`
	Capacity int     `json:"capacity"`
	Revenue  float64 `json:"revenue"`
}
