package domain

import "time"

type NewsCategory string

const (
	NewsAnnouncement NewsCategory = "ANNOUNCEMENT"
	NewsConcertNews  NewsCategory = "CONCERT_NEWS"
	NewsPromotion    NewsCategory = "PROMOTION"
	NewsTicketAlert  NewsCategory = "TICKET_ALERT"
)

func (c NewsCategory) Valid() bool {
	switch c {
	case NewsAnnouncement, NewsConcertNews, NewsPromotion, NewsTicketAlert:
		return true
	default:
		return false
	}
}

type NewsArticle struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Slug        string       `json:"slug"`
	Summary     string       `json:"summary"`
	Content     string       `json:"content"`
	CoverImage  string       `json:"cover_image"`
	Category    NewsCategory `json:"category"`
	IsPublished bool         `json:"is_published"`
	ViewsCount  int          `json:"views_count"`
	PublishedAt time.Time    `json:"published_at"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}
