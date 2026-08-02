package domain

// Seat is a physical position on an event's seat map. Status is computed at read time by
// merging the persisted layout row with live Redis state — it is never stored in Postgres.
type Seat struct {
	ID         string  `json:"id"`
	ZoneID     string  `json:"zone_id"`
	EventID    string  `json:"event_id"`
	RowLabel   string  `json:"row_label"`
	SeatNumber int     `json:"seat_number"`
	PositionX  float64 `json:"position_x"`
	PositionY  float64 `json:"position_y"`
	Status     string  `json:"status"` // AVAILABLE, HELD, SOLD
}
