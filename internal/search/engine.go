package search

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Engine runs the full search: vector search, then re-ranking of the best candidates when a re-ranker
// is set.
type Engine struct {
	Pool     *pgxpool.Pool
	Embedder QueryEmbedder
	Reranker Reranker // nil: rank by vector similarity only
	// Candidates is how many vector results are re-ranked; 0 means DefaultCandidates.
	Candidates int
}

// Search returns up to limit works matching query and f, best first.
func (e *Engine) Search(ctx context.Context, query string, f Filters, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}

	if e.Reranker == nil {
		return Semantic(ctx, e.Pool, e.Embedder, query, f, limit)
	}

	candidates := e.Candidates
	if candidates <= 0 {
		candidates = DefaultCandidates
	}

	results, err := Semantic(ctx, e.Pool, e.Embedder, query, f, max(candidates, limit))
	if err != nil {
		return nil, err
	}

	return Rerank(ctx, e.Reranker, query, results, limit)
}
