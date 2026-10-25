package bench

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/nem0z/book-finder/internal/search"
)

// Recommendation compares search methods on queries asking for a kind of book: every work each method
// returns in its top RecallDepth is judged with RecommendationRubric, so methods are scored on how relevant
// their results are rather than on finding one known book.
type Recommendation struct {
	Queries   []Query
	Methods   []string
	Orders    [][][]search.Result   // [method][query]
	Retrieval [][]time.Duration     // [method][query]; for a re-ranked method, its base method's
	Rerank    [][]time.Duration     // [method][query]; nil when the method doesn't re-rank
	Judgments []map[string]Judgment // [query] by work ID; missing when the judge failed
	// Baseline is the method the others are compared with per query; Proposed the one shown in examples.
	Baseline, Proposed int
	// Note, when set, is written above the results.
	Note string
}

// RetrieveRecommendations retrieves the top RecallDepth of every query with each method, after an untimed
// warm-up call per method.
func RetrieveRecommendations(ctx context.Context, methods []Method, queries []Query) (*Recommendation, error) {
	r := &Recommendation{Queries: queries}
	for _, m := range methods {
		if len(queries) > 0 {
			if _, err := m.Retrieve(ctx, queries[0].Query, RecallDepth); err != nil {
				return nil, fmt.Errorf("%s: %w", m.Name, err)
			}
		}

		var orders [][]search.Result
		var took []time.Duration
		for _, q := range queries {
			start := time.Now()
			list, err := m.Retrieve(ctx, q.Query, RecallDepth)
			if err != nil {
				return nil, fmt.Errorf("%s, %q: %w", m.Name, q.Query, err)
			}

			orders, took = append(orders, list), append(took, time.Since(start))
		}

		r.Methods, r.Orders = append(r.Methods, m.Name), append(r.Orders, orders)
		r.Retrieval, r.Rerank = append(r.Retrieval, took), append(r.Rerank, nil)
	}

	return r, nil
}

// Rate has judge rate every work any method returned for each query (only those not cached), then keeps
// the judgments.
func (r *Recommendation) Rate(ctx context.Context, judge *Judge) error {
	r.Judgments = make([]map[string]Judgment, len(r.Queries))
	for qi, q := range r.Queries {
		var union []search.Result
		seen := map[string]bool{}
		for _, orders := range r.Orders {
			for _, w := range orders[qi] {
				if !seen[w.ID] {
					seen[w.ID] = true
					union = append(union, w)
				}
			}
		}

		if err := judge.RateAll(ctx, q.Query, union); err != nil {
			return err
		}

		r.Judgments[qi] = map[string]Judgment{}
		for _, w := range union {
			if jd, ok := judge.Judgment(q.Query, w); ok {
				r.Judgments[qi][w.ID] = jd
			}
		}

		cached, asked, failed := judge.Stats()
		judge.logger().Info("rated", "query", qi+1, "of", len(r.Queries), "works", len(union), "cached", cached, "asked", asked, "failed", failed)
	}

	return nil
}

// RerankMethod re-ranks the top depth of method base's results with rr, for every query, and returns the
// orders and the re-rank times. It only reads r, so it can run while Rate does.
func (r *Recommendation) RerankMethod(ctx context.Context, rr search.Reranker, base, depth int) ([][]search.Result, []time.Duration, error) {
	var orders [][]search.Result
	var took []time.Duration
	for qi, q := range r.Queries {
		list := r.Orders[base][qi]
		d := min(depth, len(list))
		start, spent := time.Now(), spentBy(rr)
		top, err := search.Rerank(ctx, rr, q.Query, list[:d], d)
		if err != nil {
			return nil, nil, fmt.Errorf("%q: %w", q.Query, err)
		}

		t := time.Since(start)
		if s := spentBy(rr); s >= 0 {
			t = s - spent
		}

		orders, took = append(orders, append(top, list[d:]...)), append(took, t)
	}

	return orders, took, nil
}

// AddMethod adds a method whose orders come from re-ranking method base's.
func (r *Recommendation) AddMethod(name string, base int, orders [][]search.Result, took []time.Duration) {
	r.Methods, r.Orders = append(r.Methods, name), append(r.Orders, orders)
	r.Retrieval, r.Rerank = append(r.Retrieval, r.Retrieval[base]), append(r.Rerank, took)
}

// rating returns the judge's rating of w for query qi.
func (r *Recommendation) rating(qi int, w search.Result) (int, bool) {
	jd, ok := r.Judgments[qi][w.ID]
	return jd.Rating, ok
}

// relevance is the mean rating and the share rated 2 or more of method m's results ranked from+1 to to,
// over the queries keep accepts, and how many results were rated.
func (r *Recommendation) relevance(m, from, to int, keep func(qi int) bool) (mean, good float64, rated int) {
	sum, goods := 0, 0
	for qi := range r.Queries {
		if !keep(qi) {
			continue
		}

		list := r.Orders[m][qi]
		for _, w := range list[min(from, len(list)):min(to, len(list))] {
			if x, ok := r.rating(qi, w); ok {
				sum += x
				rated++
				if x >= 2 {
					goods++
				}
			}
		}
	}

	if rated > 0 {
		mean, good = float64(sum)/float64(rated), float64(goods)/float64(rated)
	}

	return mean, good, rated
}

// ndcg10 is method m's nDCG@10 for query qi, gain 2^rating − 1, against the best 10 of all the works rated
// for the query, so that methods are compared on the same scale. It returns -1 when no work is rated above 0.
func (r *Recommendation) ndcg10(m, qi int) float64 {
	dcg := func(ratings []int) float64 {
		s := 0.0
		for i, x := range ratings[:min(10, len(ratings))] {
			s += (math.Pow(2, float64(x)) - 1) / math.Log2(float64(i+2))
		}

		return s
	}

	var ideal []int
	for _, jd := range r.Judgments[qi] {
		ideal = append(ideal, jd.Rating)
	}

	slices.SortFunc(ideal, func(a, b int) int { return b - a })
	best := dcg(ideal)
	if best == 0 {
		return -1
	}

	var got []int
	for _, w := range r.Orders[m][qi][:min(10, len(r.Orders[m][qi]))] {
		x, _ := r.rating(qi, w)
		got = append(got, x)
	}

	return dcg(got) / best
}

// meanNDCG10 is method m's mean nDCG@10 over the queries keep accepts that have a work rated above 0.
func (r *Recommendation) meanNDCG10(m int, keep func(qi int) bool) float64 {
	sum, n := 0.0, 0
	for qi := range r.Queries {
		if x := r.ndcg10(m, qi); keep(qi) && x >= 0 {
			sum += x
			n++
		}
	}

	if n == 0 {
		return 0
	}

	return sum / float64(n)
}

// dimension is method m's mean score on dimension d over its top 10 results, where the judge found the
// dimension asked for, the share of those results scored 0, and how many there were.
func (r *Recommendation) dimension(m int, d string) (mean, zero float64, n int) {
	sum, zeros := 0, 0
	for qi := range r.Queries {
		for _, w := range r.Orders[m][qi][:min(10, len(r.Orders[m][qi]))] {
			jd, ok := r.Judgments[qi][w.ID]
			if s, asked := jd.Dimensions[d]; ok && asked && s != NotApplicable {
				sum += s
				n++
				if s == 0 {
					zeros++
				}
			}
		}
	}

	if n > 0 {
		mean, zero = float64(sum)/float64(n), float64(zeros)/float64(n)
	}

	return mean, zero, n
}
