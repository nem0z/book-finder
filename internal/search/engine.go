package search

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultInterpretTimeout bounds the Interpreter call. A small hosted model answers in about a second;
// local models on CPU need more (see Engine.InterpretTimeout).
const DefaultInterpretTimeout = 5 * time.Second

// Engine runs the full search: reading the query when an interpreter is set, vector search, then
// re-ranking of the best candidates when a re-ranker is set.
type Engine struct {
	Pool     *pgxpool.Pool
	Embedder QueryEmbedder
	Reranker Reranker // nil: rank by vector similarity only
	// Candidates is how many vector results are re-ranked; 0 means DefaultCandidates.
	Candidates int
	// Interpreter reads the query first: its description is embedded in place of the query, and its
	// filters apply where the caller set none. The re-ranker still reads the original query. nil: the
	// query is embedded as is.
	Interpreter Interpreter
	// InterpretTimeout is 0 for DefaultInterpretTimeout. When the Interpreter fails or times out, the
	// search goes on with the original query.
	InterpretTimeout time.Duration
	Log              *slog.Logger // nil: slog.Default()
}

// Search returns up to limit works matching query and f, best first.
func (e *Engine) Search(ctx context.Context, query string, f Filters, limit int) ([]Result, error) {
	results, _, err := e.SearchInterpreted(ctx, query, f, limit)
	return results, err
}

// SearchInterpreted is Search, also returning the description and filters the search used.
func (e *Engine) SearchInterpreted(ctx context.Context, query string, f Filters, limit int) ([]Result, Interpretation, error) {
	if strings.TrimSpace(query) == "" {
		return nil, Interpretation{}, ErrEmptyQuery
	}

	if limit <= 0 {
		limit = DefaultLimit
	}

	in := e.interpret(ctx, query, f)
	if e.Reranker == nil {
		results, err := Semantic(ctx, e.Pool, e.Embedder, in.Description, in.Filters, limit)
		return results, in, err
	}

	candidates := e.Candidates
	if candidates <= 0 {
		candidates = DefaultCandidates
	}

	results, err := Semantic(ctx, e.Pool, e.Embedder, in.Description, in.Filters, max(candidates, limit))
	if err != nil {
		return nil, in, err
	}

	results, err = Rerank(ctx, e.Reranker, query, results, limit)
	return results, in, err
}

// interpret returns what to search for: the query and f as they are without an Interpreter or when it
// fails, else its description and its filters merged into f.
func (e *Engine) interpret(ctx context.Context, query string, f Filters) Interpretation {
	in := Interpretation{Filters: f, Description: query}
	if e.Interpreter == nil {
		return in
	}

	timeout := e.InterpretTimeout
	if timeout <= 0 {
		timeout = DefaultInterpretTimeout
	}

	ictx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	got, err := e.Interpreter.Interpret(ictx, query)
	if err != nil {
		if ctx.Err() == nil {
			e.log().Warn("query not interpreted, searching with it as is", "err", err)
		}

		return in
	}

	if d := strings.TrimSpace(got.Description); d != "" {
		in.Description = d
	}

	in.Filters = merge(f, got.Filters)

	return in
}

func (e *Engine) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}

	return slog.Default()
}

// merge adds the interpreted filters to the caller's. The caller's win: a year or page range the caller
// bounded on either side is kept whole, so the two can't combine into a contradiction.
func merge(caller, interpreted Filters) Filters {
	out := caller
	if caller.MinYear == 0 && caller.MaxYear == 0 {
		out.MinYear, out.MaxYear = interpreted.MinYear, interpreted.MaxYear
	}

	if caller.MinPages == 0 && caller.MaxPages == 0 {
		out.MinPages, out.MaxPages = interpreted.MinPages, interpreted.MaxPages
	}

	if caller.OriginalLanguage == "" {
		out.OriginalLanguage = interpreted.OriginalLanguage
	}

	return out
}
