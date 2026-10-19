package search

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	// DefaultCandidates is how many vector results are re-ranked. Measured on this project's test
	// queries with gte-reranker-modernbert-base: 30 ranks as well as 50, in two thirds of the time.
	DefaultCandidates = 30

	// cardDescriptionBytes is how much of the description the re-ranker reads. 600 bytes kept the full
	// quality gain; 300 lost half of it; 2,000 doubled the time for no gain.
	cardDescriptionBytes = 600
)

// Reranker scores how well each text matches the query, in input order (higher is better).
// *rerank.TEI implements it.
type Reranker interface {
	Scores(ctx context.Context, query string, texts []string) ([]float64, error)
}

// Rerank re-orders results by the re-ranker's score and keeps the best limit. The re-ranker reads a
// short card per book: title, authors, year, pages and the start of the description.
func Rerank(ctx context.Context, rr Reranker, query string, results []Result, limit int) ([]Result, error) {
	if len(results) == 0 {
		return results, nil
	}

	cards := make([]string, len(results))
	for i, r := range results {
		cards[i] = card(r)
	}

	scores, err := rr.Scores(ctx, query, cards)
	if err != nil {
		return nil, fmt.Errorf("re-rank: %w", err)
	}

	if len(scores) != len(results) {
		return nil, fmt.Errorf("re-rank: %d scores for %d results", len(scores), len(results))
	}

	out := slices.Clone(results)
	for i := range out {
		out[i].RerankScore = scores[i]
	}

	slices.SortStableFunc(out, func(a, b Result) int { return cmpDesc(a.RerankScore, b.RerankScore) })

	return out[:min(limit, len(out))], nil
}

// card is the text the re-ranker reads for one book.
func card(r Result) string {
	var b strings.Builder
	b.WriteString(r.Title)
	if len(r.Authors) > 0 {
		b.WriteString(" by " + strings.Join(r.Authors, ", "))
	}

	// Year and pages help when the query mentions an era or a length. Tags were tried too: they did
	// not improve the ranking on the test queries.
	var facts []string
	if r.Year > 0 {
		facts = append(facts, strconv.Itoa(r.Year))
	}

	if r.Pages > 0 {
		facts = append(facts, strconv.Itoa(r.Pages)+" pages")
	}

	if len(facts) > 0 {
		b.WriteString("\n" + strings.Join(facts, ", "))
	}

	b.WriteString("\n\n" + cut(r.Description, cardDescriptionBytes))

	return b.String()
}

// cut shortens s to at most max bytes, at a word boundary when possible.
func cut(s string, max int) string {
	if len(s) <= max {
		return s
	}

	if i := strings.LastIndexByte(s[:max], ' '); i > 0 {
		max = i
	}

	return strings.ToValidUTF8(s[:max], "")
}

func cmpDesc(a, b float64) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	default:
		return 0
	}
}
