package bench

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/nem0z/book-finder/internal/search"
)

const (
	// DefaultDepth is how many retrieved works are rated per query: the pool and the works below it, which
	// tell a retrieval miss from a gap in the catalogue.
	DefaultDepth = 100

	// relevant is the lowest rating of a relevant work.
	relevant = 2
)

// Eval is the search evaluated in two stages. Retrieval finds each query's candidates; the first Pool of
// them are re-ranked, and the top 10 after re-ranking is what a reader sees. Every retrieved work is
// rated by the judge.
type Eval struct {
	Queries   []Query
	Pool      int
	Retrieved [][]search.Result // [query] the top Depth, in retrieval order
	Final     [][]search.Result // [query] the pool re-ranked, then the rest; nil without re-ranking
	Retrieval []time.Duration   // [query]
	Rerank    []time.Duration   // [query]
	Judgments []map[string]Judgment
	// RerankQueries, when set, is the text the re-ranker reads for each query instead of the query itself.
	RerankQueries []string
	// Note, when set, is written above the report: what was searched and rated, how.
	Note string
}

// RetrieveFunc returns the n best candidates for query, best first: search.Engine.Retrieve, with filters.
type RetrieveFunc func(ctx context.Context, query string, n int) ([]search.Result, error)

// queryStats is how one query fares.
type queryStats struct {
	qi                  int
	poolGood, top10Good int     // relevant works in the pool and in the final top 10
	belowGood           int     // relevant works retrieved below the pool
	top10Mean           float64 // mean rating of the final top 10
}

// Retrieve retrieves the top depth of every query, after an untimed warm-up call. pool is how many of them
// are re-ranked.
func Retrieve(ctx context.Context, retrieve RetrieveFunc, queries []Query, pool, depth int) (*Eval, error) {
	ev := &Eval{Queries: queries, Pool: pool}
	if len(queries) > 0 {
		if _, err := retrieve(ctx, queries[0].Query, depth); err != nil {
			return nil, err
		}
	}

	for _, q := range queries {
		start := time.Now()
		list, err := retrieve(ctx, q.Query, depth)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", q.Query, err)
		}

		ev.Retrieved, ev.Retrieval = append(ev.Retrieved, list), append(ev.Retrieval, time.Since(start))
	}

	return ev, nil
}

// Rate has judge rate every retrieved work (only those not cached), then keeps the judgments.
func (ev *Eval) Rate(ctx context.Context, judge *Judge) error {
	ev.Judgments = make([]map[string]Judgment, len(ev.Queries))
	for qi, q := range ev.Queries {
		if err := judge.RateAll(ctx, q.Query, ev.Retrieved[qi]); err != nil {
			return err
		}

		ev.Judgments[qi] = map[string]Judgment{}
		for _, w := range ev.Retrieved[qi] {
			if jd, ok := judge.Judgment(q.Query, w); ok {
				ev.Judgments[qi][w.ID] = jd
			}
		}

		cached, asked, failed := judge.Stats()
		judge.logger().Info("rated", "query", qi+1, "of", len(ev.Queries), "cached", cached, "asked", asked, "failed", failed)
	}

	return nil
}

// RerankPool re-ranks every query's pool with rr, as the search does, and sets Final and Rerank. It only
// reads Retrieved, so it can run while Rate does.
func (ev *Eval) RerankPool(ctx context.Context, rr search.Reranker) error {
	final := make([][]search.Result, len(ev.Queries))
	took := make([]time.Duration, len(ev.Queries))
	for qi, q := range ev.Queries {
		list := ev.Retrieved[qi]
		n := min(ev.Pool, len(list))
		text := q.Query
		if ev.RerankQueries != nil {
			text = ev.RerankQueries[qi]
		}

		start, spent := time.Now(), spentBy(rr)
		top, err := search.Rerank(ctx, rr, text, list[:n], n)
		if err != nil {
			return fmt.Errorf("%q: %w", q.Query, err)
		}

		took[qi] = time.Since(start)
		if s := spentBy(rr); s >= 0 {
			took[qi] = s - spent
		}

		final[qi] = append(top, list[n:]...)
	}

	ev.Final, ev.Rerank = final, took

	return nil
}

// final returns query qi's order after re-ranking, or its retrieval order without re-ranking.
func (ev *Eval) final(qi int) []search.Result {
	if ev.Final == nil {
		return ev.Retrieved[qi]
	}

	return ev.Final[qi]
}

func (ev *Eval) retrieved(qi int) []search.Result { return ev.Retrieved[qi] }

// rating returns the judge's rating of w for query qi.
func (ev *Eval) rating(qi int, w search.Result) (int, bool) {
	jd, ok := ev.Judgments[qi][w.ID]
	return jd.Rating, ok
}

// ratings returns the shares of order's ranks from+1 to to rated relevant, 3 and 0, and their mean rating,
// over the queries keep accepts, and how many works were rated.
func (ev *Eval) ratings(order func(int) []search.Result, from, to int, keep func(int) bool) (mean, good, three, zero float64, rated int) {
	sum, goods, threes, zeros := 0, 0, 0, 0
	for qi := range ev.Queries {
		if !keep(qi) {
			continue
		}

		list := order(qi)
		for _, w := range list[min(from, len(list)):min(to, len(list))] {
			x, ok := ev.rating(qi, w)
			if !ok {
				continue
			}

			sum += x
			rated++
			if x >= relevant {
				goods++
			}

			if x == MaxRating {
				threes++
			}

			if x == 0 {
				zeros++
			}
		}
	}

	if rated == 0 {
		return 0, 0, 0, 0, 0
	}

	n := float64(rated)

	return float64(sum) / n, float64(goods) / n, float64(threes) / n, float64(zeros) / n, rated
}

// ndcg10 is order's nDCG@10 for query qi, gain 2^rating − 1, against the best 10 of all the works rated for
// the query. It returns -1 when no work is rated above 0.
func (ev *Eval) ndcg10(order func(int) []search.Result, qi int) float64 {
	dcg := func(ratings []int) float64 {
		s := 0.0
		for i, x := range ratings[:min(10, len(ratings))] {
			s += (math.Pow(2, float64(x)) - 1) / math.Log2(float64(i+2))
		}

		return s
	}

	var ideal []int
	for _, jd := range ev.Judgments[qi] {
		ideal = append(ideal, jd.Rating)
	}

	slices.SortFunc(ideal, func(a, b int) int { return b - a })
	best := dcg(ideal)
	if best == 0 {
		return -1
	}

	var got []int
	list := order(qi)
	for _, w := range list[:min(10, len(list))] {
		x, _ := ev.rating(qi, w)
		got = append(got, x)
	}

	return dcg(got) / best
}

// meanNDCG10 is order's mean nDCG@10 over the queries keep accepts that have a work rated above 0.
func (ev *Eval) meanNDCG10(order func(int) []search.Result, keep func(int) bool) float64 {
	sum, n := 0.0, 0
	for qi := range ev.Queries {
		if x := ev.ndcg10(order, qi); keep(qi) && x >= 0 {
			sum += x
			n++
		}
	}

	if n == 0 {
		return 0
	}

	return sum / float64(n)
}

// dimension is the mean score on dimension d of order's top depth, where the query asks about it, the share
// of those works scored 0, and how many there were.
func (ev *Eval) dimension(order func(int) []search.Result, d string, depth int) (mean, zero float64, n int) {
	sum, zeros := 0, 0
	for qi := range ev.Queries {
		list := order(qi)
		for _, w := range list[:min(depth, len(list))] {
			jd, ok := ev.Judgments[qi][w.ID]
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

// stats returns how query qi fares.
func (ev *Eval) stats(qi int) queryStats {
	s := queryStats{qi: qi}
	for k, w := range ev.Retrieved[qi] {
		if x, ok := ev.rating(qi, w); ok && x >= relevant {
			if k < ev.Pool {
				s.poolGood++
			} else {
				s.belowGood++
			}
		}
	}

	sum, n := 0, 0
	list := ev.final(qi)
	for _, w := range list[:min(10, len(list))] {
		if x, ok := ev.rating(qi, w); ok {
			sum += x
			n++
			if x >= relevant {
				s.top10Good++
			}
		}
	}

	if n > 0 {
		s.top10Mean = float64(sum) / float64(n)
	}

	return s
}

// possible is how many top-10 slots the pool could fill with relevant works.
func (s queryStats) possible() int { return min(10, s.poolGood) }

// causeKind says why a query's top 10 is poor: the re-ranking leaves relevant works of the pool out
// ("ranking"), the pool misses relevant works retrieved below it ("retrieval"), or retrieval finds few at all,
// which suggests a gap in the catalogue, worth checking by hand ("catalogue gap?").
func (s queryStats) causeKind() string {
	switch {
	case s.possible()-s.top10Good >= 3:
		return "ranking"
	case s.poolGood < 10 && s.belowGood >= 3:
		return "retrieval"
	default:
		return "catalogue gap?"
	}
}

// cause is causeKind with the counts behind it.
func (s queryStats) cause() string {
	switch k := s.causeKind(); k {
	case "ranking":
		return fmt.Sprintf("%s: the pool has %d relevant works, the top 10 only %d", k, s.poolGood, s.top10Good)
	case "retrieval":
		return fmt.Sprintf("%s: %d relevant works retrieved below the pool, %d in it", k, s.belowGood, s.poolGood)
	default:
		return fmt.Sprintf("%s %d relevant works in the pool, %d below it", k, s.poolGood, s.belowGood)
	}
}

// spentBy returns the time rr spent in calls, for re-rankers that wait between calls (PacedReranker); -1
// for others.
func spentBy(rr search.Reranker) time.Duration {
	if s, ok := rr.(interface{ Spent() time.Duration }); ok {
		return s.Spent()
	}

	return -1
}

func all(int) bool { return true }
