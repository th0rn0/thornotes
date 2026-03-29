package notes

import (
	"context"

	"github.com/th0rn0/thornotes/internal/model"
)

// Search performs full-text search for userID.
func (s *Service) Search(ctx context.Context, userID int64, query string, tags []string) ([]*model.SearchResult, error) {
	return s.search.Search(ctx, userID, query, tags)
}
