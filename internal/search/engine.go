package search

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Engine runs the full search: the works whose keyword vector is nearest to the query's, then re-ranking of
// the best candidates when a re-ranker is set.
type Engine struct {
	Pool     *pgxpool.Pool
	Embedder QueryEmbedder
	Reranker Reranker // nil: keep the retrieval order
	// Candidates is how many retrieved works are re-ranked; 0 means DefaultCandidates.
	Candidates int
}

// Search returns up to limit works matching query and f, best first.
func (e *Engine) Search(ctx context.Context, query string, f Filters, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}

	if e.Reranker == nil {
		return e.Retrieve(ctx, query, f, limit)
	}

	candidates := e.Candidates
	if candidates <= 0 {
		candidates = DefaultCandidates
	}

	results, err := e.Retrieve(ctx, query, f, max(candidates, limit))
	if err != nil {
		return nil, err
	}

	return Rerank(ctx, e.Reranker, query, results, limit)
}

// Retrieve returns the n best candidates for query and f, before re-ranking: the works whose keyword vector is
// nearest to the query's.
func (e *Engine) Retrieve(ctx context.Context, query string, f Filters, n int) ([]Result, error) {
	return KeywordVectors(ctx, e.Pool, e.Embedder, query, f, n)
}
