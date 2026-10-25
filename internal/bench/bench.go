// Package bench measures how well search pipelines find known books. Each query describes a book
// without naming it, and a pipeline scores when it ranks one of the book's works high.
package bench

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/nem0z/book-finder/internal/search"
)

// RecallDepth is how many candidates are searched for the target, before re-ranking.
const RecallDepth = 100

var (
	//go:embed queries.json
	builtinQueries []byte

	//go:embed recommendation-queries.json
	builtinRecommendationQueries []byte
)

var ErrNoQueries = errors.New("no queries")

// Query describes a book. Targets are the Open Library IDs of the book's works (duplicates,
// omnibuses); a query without targets is judged by reading its results.
type Query struct {
	Query   string   `json:"query"`
	Kind    string   `json:"kind"`
	Title   string   `json:"title,omitempty"`
	Targets []string `json:"targets,omitempty"`
	// Recommendation queries have a category and a length (S, M, L, XL); Flag notes a known catalogue
	// limit that may sink a query whatever the search.
	Category string `json:"category,omitempty"`
	Length   string `json:"length,omitempty"`
	Flag     string `json:"flag,omitempty"`
}

// RetrieveFunc returns the n best candidates for query, best first.
type RetrieveFunc func(ctx context.Context, query string, n int) ([]search.Result, error)

// Pipeline retrieves candidates; the re-ranker then re-orders the first Candidates of them.
type Pipeline struct {
	Name     string
	Retrieve RetrieveFunc
	// Candidates is how many candidates are re-ranked; 0 means search.DefaultCandidates.
	Candidates int
}

// Outcome is one pipeline's answer to one query. Ranks are 1-based, 0 when no target is there.
type Outcome struct {
	RetrievalRank int             // in the first RecallDepth candidates
	FinalRank     int             // in Results
	Results       []search.Result // after re-ranking
	Retrieval     time.Duration   // to retrieve max(RecallDepth, Candidates) candidates
	Rerank        time.Duration
}

// Summary aggregates a pipeline's outcomes over the queries with targets. Times are means.
type Summary struct {
	Queries   int
	Found     int // target in the first RecallDepth candidates
	Hit1      int
	HitAll    int     // target anywhere in the results
	MRR       float64 // mean of 1/FinalRank, counting 0 when the target is missing
	Retrieval time.Duration
	Rerank    time.Duration
}

// Report holds every pipeline's outcomes: Outcomes[pipeline][query].
type Report struct {
	Queries   []Query
	Pipelines []Pipeline
	Limit     int
	Outcomes  [][]Outcome
	// Note, when set, is written above the results: what the pipelines searched, for example.
	Note string
}

// LoadQueries reads queries from a JSON file, or returns the built-in ones when path is empty.
func LoadQueries(path string) ([]Query, error) {
	return loadQueries(path, builtinQueries)
}

// LoadRecommendationQueries reads queries from a JSON file, or returns the built-in recommendation queries
// when path is empty: readers asking for a kind of book, not describing one.
func LoadRecommendationQueries(path string) ([]Query, error) {
	return loadQueries(path, builtinRecommendationQueries)
}

func loadQueries(path string, builtin []byte) ([]Query, error) {
	data := builtin
	if path != "" {
		var err error
		if data, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}

	var qs []Query
	if err := json.Unmarshal(data, &qs); err != nil {
		return nil, fmt.Errorf("queries: %w", err)
	}

	if len(qs) == 0 {
		return nil, ErrNoQueries
	}

	return qs, nil
}

// Run runs every query through every pipeline. rr may be nil to rank by retrieval only.
func Run(ctx context.Context, rr search.Reranker, pipelines []Pipeline, queries []Query, limit int) (Report, error) {
	if limit <= 0 {
		limit = search.DefaultLimit
	}

	rep := Report{Queries: queries, Pipelines: slices.Clone(pipelines), Limit: limit, Outcomes: make([][]Outcome, len(pipelines))}
	for i := range rep.Pipelines {
		p := &rep.Pipelines[i]
		if p.Candidates <= 0 {
			p.Candidates = search.DefaultCandidates
		}

		for _, q := range queries {
			o, err := runOne(ctx, rr, *p, q, limit)
			if err != nil {
				return rep, fmt.Errorf("%s, %q: %w", p.Name, q.Query, err)
			}

			rep.Outcomes[i] = append(rep.Outcomes[i], o)
		}
	}

	return rep, nil
}

func runOne(ctx context.Context, rr search.Reranker, p Pipeline, q Query, limit int) (Outcome, error) {
	var o Outcome
	start := time.Now()
	cands, err := p.Retrieve(ctx, q.Query, max(RecallDepth, p.Candidates))
	if err != nil {
		return o, err
	}

	o.Retrieval = time.Since(start)
	o.RetrievalRank = rank(cands[:min(RecallDepth, len(cands))], q.Targets)

	cands = cands[:min(p.Candidates, len(cands))]
	if rr == nil {
		o.Results = cands[:min(limit, len(cands))]
	} else {
		start = time.Now()
		if o.Results, err = search.Rerank(ctx, rr, q.Query, cands, limit); err != nil {
			return o, err
		}

		o.Rerank = time.Since(start)
	}

	o.FinalRank = rank(o.Results, q.Targets)

	return o, nil
}

// Summarize aggregates pipeline i's outcomes.
func (r Report) Summarize(i int) Summary {
	var s Summary
	var retrieval, rerank time.Duration
	for j, o := range r.Outcomes[i] {
		retrieval += o.Retrieval
		rerank += o.Rerank
		if len(r.Queries[j].Targets) == 0 {
			continue
		}

		s.Queries++
		if o.RetrievalRank > 0 {
			s.Found++
		}

		if o.FinalRank > 0 {
			s.HitAll++
			s.MRR += 1 / float64(o.FinalRank)
		}

		if o.FinalRank == 1 {
			s.Hit1++
		}
	}

	if n := len(r.Outcomes[i]); n > 0 {
		s.Retrieval, s.Rerank = retrieval/time.Duration(n), rerank/time.Duration(n)
	}

	if s.Queries > 0 {
		s.MRR /= float64(s.Queries)
	}

	return s
}

// rank returns the 1-based position of the first result that is a target, 0 if none is.
func rank(results []search.Result, targets []string) int {
	for i, r := range results {
		if slices.Contains(targets, r.ID) {
			return i + 1
		}
	}

	return 0
}
