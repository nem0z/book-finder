package search

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Retrieval is how the Engine finds candidates before re-ranking.
type Retrieval string

const (
	RetrieveVector   Retrieval = "vector"   // nearest EmbeddingGemma vectors (Semantic); the default
	RetrieveKeywords Retrieval = "keywords" // LLM keywords matching the query's words (Keywords)
	RetrieveFullText Retrieval = "fulltext" // title, authors, subjects and description matching them (FullText)
	// nearest vectors of the LLM keywords (KeywordVectors)
	RetrieveKeywordVectors Retrieval = "keyword-vectors"
)

var ErrUnknownRetrieval = errors.New("unknown retrieval")

// Engine runs the full search: retrieval, then re-ranking of the best candidates when a re-ranker is set.
type Engine struct {
	Pool      *pgxpool.Pool
	Embedder  QueryEmbedder // used by vector retrieval
	Retrieval Retrieval     // "" means RetrieveVector
	Reranker  Reranker      // nil: keep the retrieval order
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

// Retrieve returns the n best candidates for query and f, by e's retrieval.
func (e *Engine) Retrieve(ctx context.Context, query string, f Filters, n int) ([]Result, error) {
	switch e.Retrieval {
	case "", RetrieveVector:
		return Semantic(ctx, e.Pool, e.Embedder, query, f, n)
	case RetrieveKeywords:
		return Keywords(ctx, e.Pool, query, f, n)
	case RetrieveFullText:
		return FullText(ctx, e.Pool, query, f, n)
	case RetrieveKeywordVectors:
		return KeywordVectors(ctx, e.Pool, e.Embedder, query, f, n)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownRetrieval, e.Retrieval)
	}
}
