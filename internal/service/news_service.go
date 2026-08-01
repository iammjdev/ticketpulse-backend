package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/iammjdev/ticketpulse-backend/internal/domain"
	"github.com/iammjdev/ticketpulse-backend/internal/repository"
)

const (
	newsLatestCacheKey  = "news:latest"
	newsSlugCacheTTL    = 30 * time.Minute
	newsSlugCachePrefix = "news:slug:"
)

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

type newsListCachePayload struct {
	Articles []*domain.NewsArticle `json:"articles"`
	Total    int                   `json:"total"`
}

// NewsService wraps NewsRepository with a Redis read-through cache: the default
// (page 1, unfiltered, published-only) listing is cached under news:latest, and each
// article detail is cached under news:slug:{slug} for 30 minutes. Both are invalidated
// on any admin mutation rather than left to expire.
type NewsService struct {
	repo repository.NewsRepository
	rdb  *redis.Client
}

func NewNewsService(repo repository.NewsRepository, rdb *redis.Client) *NewsService {
	return &NewsService{repo: repo, rdb: rdb}
}

func isDefaultListQuery(filter repository.NewsListFilter) bool {
	return filter.PublishedOnly && filter.Category == "" && filter.Search == "" && (filter.Page == 0 || filter.Page == 1)
}

// List serves the default front-page query from Redis when present, and always falls
// back to PostgreSQL for filtered/paginated/admin queries.
func (s *NewsService) List(ctx context.Context, filter repository.NewsListFilter) ([]*domain.NewsArticle, int, error) {
	useCache := isDefaultListQuery(filter)

	if useCache {
		if cached, err := s.rdb.Get(ctx, newsLatestCacheKey).Result(); err == nil {
			var payload newsListCachePayload
			if json.Unmarshal([]byte(cached), &payload) == nil {
				return payload.Articles, payload.Total, nil
			}
		}
	}

	articles, total, err := s.repo.ListNews(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	if useCache {
		if encoded, err := json.Marshal(newsListCachePayload{Articles: articles, Total: total}); err == nil {
			_ = s.rdb.Set(ctx, newsLatestCacheKey, encoded, 0).Err()
		}
	}

	return articles, total, nil
}

// GetBySlug serves the cached article detail when present; otherwise it reads through to
// PostgreSQL and populates the cache. View counts are bumped in the background so the read
// path never waits on the write.
func (s *NewsService) GetBySlug(ctx context.Context, slug string, publishedOnly bool) (*domain.NewsArticle, error) {
	cacheKey := newsSlugCachePrefix + slug

	if cached, err := s.rdb.Get(ctx, cacheKey).Result(); err == nil {
		var article domain.NewsArticle
		if json.Unmarshal([]byte(cached), &article) == nil {
			if !publishedOnly || article.IsPublished {
				s.bumpViewsAsync(article.ID)
				return &article, nil
			}
		}
	}

	article, err := s.repo.FindBySlug(ctx, slug, publishedOnly)
	if err != nil {
		return nil, err
	}

	if encoded, err := json.Marshal(article); err == nil {
		_ = s.rdb.Set(ctx, cacheKey, encoded, newsSlugCacheTTL).Err()
	}

	s.bumpViewsAsync(article.ID)
	return article, nil
}

func (s *NewsService) bumpViewsAsync(articleID string) {
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.repo.IncrementViews(bgCtx, articleID)
	}()
}

// GenerateUniqueSlug slugifies title and appends -2, -3, ... until it no longer collides.
func (s *NewsService) GenerateUniqueSlug(ctx context.Context, title string) (string, error) {
	base := slugify(title)
	if base == "" {
		base = "article"
	}

	slug := base
	for suffix := 2; ; suffix++ {
		exists, err := s.repo.SlugExists(ctx, slug)
		if err != nil {
			return "", err
		}
		if !exists {
			return slug, nil
		}
		slug = base + "-" + strconv.Itoa(suffix)
	}
}

func slugify(title string) string {
	lower := strings.ToLower(strings.TrimSpace(title))
	slug := slugNonAlnum.ReplaceAllString(lower, "-")
	return strings.Trim(slug, "-")
}

func (s *NewsService) GetByID(ctx context.Context, id string) (*domain.NewsArticle, error) {
	return s.repo.FindByID(ctx, id)
}

func (s *NewsService) SlugTaken(ctx context.Context, slug string) (bool, error) {
	return s.repo.SlugExists(ctx, slug)
}

func (s *NewsService) Create(ctx context.Context, article repository.NewNewsArticle) (*domain.NewsArticle, error) {
	created, err := s.repo.Create(ctx, article)
	if err != nil {
		return nil, err
	}
	s.invalidateList(ctx)
	return created, nil
}

func (s *NewsService) Update(ctx context.Context, id string, prevSlug string, patch repository.NewsUpdate) (*domain.NewsArticle, error) {
	updated, err := s.repo.Update(ctx, id, patch)
	if err != nil {
		return nil, err
	}
	s.invalidateList(ctx)
	s.invalidateSlug(ctx, prevSlug)
	if updated.Slug != prevSlug {
		s.invalidateSlug(ctx, updated.Slug)
	}
	return updated, nil
}

func (s *NewsService) Delete(ctx context.Context, id string, slug string) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	s.invalidateList(ctx)
	s.invalidateSlug(ctx, slug)
	return nil
}

func (s *NewsService) invalidateList(ctx context.Context) {
	_ = s.rdb.Del(ctx, newsLatestCacheKey).Err()
}

func (s *NewsService) invalidateSlug(ctx context.Context, slug string) {
	_ = s.rdb.Del(ctx, fmt.Sprintf("%s%s", newsSlugCachePrefix, slug)).Err()
}
