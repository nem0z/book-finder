package bench

import (
	"context"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/nem0z/book-finder/internal/search"
)

// bandNames name the bands of ranks of Depths.
var bandNames = []string{"1–10", "11–30", "31–100"}

// RerankEffect shows what a re-ranker does to a retrieval's top RecallDepth: for every query, the order
// before re-ranking, the order after re-ranking the top Depths[v] of it (the rest keeps its place), and
// the judge's rating of every work.
type RerankEffect struct {
	Queries []Query
	Depths  []int
	Before  [][]search.Result   // [query]
	After   [][][]search.Result // [variant][query]
	Ratings []map[string]int    // [query] work ID → rating; missing when the judge failed
	Took    [][]time.Duration   // [variant][query] re-ranking time
	Tokens  [][]int             // [variant][query] estimated tokens billed for re-ranking alone
	// Note, when set, is written above the results.
	Note string
}

// tokenCounter estimates the tokens of the last call to a re-ranker.
type tokenCounter struct {
	search.Reranker
	last int
}

func (t *tokenCounter) Scores(ctx context.Context, query string, texts []string) ([]float64, error) {
	t.last = estimateTokens(query, texts)
	return t.Reranker.Scores(ctx, query, texts)
}

// move is one work's rank before and after re-ranking, 0-based.
type move struct {
	query, before, after, rating int
	work                         search.Result
}

// RunRerankEffect retrieves the top RecallDepth of every query, has judge rate the results (only those
// not cached), and re-ranks the top depth of them with rr, for each of depths. With a PacedReranker, each
// work is scored once whatever the depths, so depths can be many.
func RunRerankEffect(ctx context.Context, judge *Judge, rr search.Reranker, retrieve RetrieveFunc, queries []Query, depths []int) (RerankEffect, error) {
	e := RerankEffect{Queries: queries, Depths: depths, After: make([][][]search.Result, len(depths)),
		Took: make([][]time.Duration, len(depths)), Tokens: make([][]int, len(depths))}
	counter := &tokenCounter{Reranker: rr}
	for qi, q := range queries {
		list, err := retrieve(ctx, q.Query, RecallDepth)
		if err != nil {
			return e, fmt.Errorf("%q: %w", q.Query, err)
		}

		if err := judge.RateAll(ctx, q.Query, list); err != nil {
			return e, err
		}

		ratings := map[string]int{}
		for _, b := range list {
			if r, ok := judge.Rating(q.Query, b); ok {
				ratings[b.ID] = r
			}
		}

		e.Before, e.Ratings = append(e.Before, list), append(e.Ratings, ratings)
		for vi, d := range depths {
			d = min(d, len(list))
			start, spent := time.Now(), spentBy(rr)
			top, err := search.Rerank(ctx, counter, q.Query, list[:d], d)
			if err != nil {
				return e, fmt.Errorf("%q: %w", q.Query, err)
			}

			took := time.Since(start)
			if s := spentBy(rr); s >= 0 {
				took = s - spent
			}

			e.Took[vi] = append(e.Took[vi], took)
			e.Tokens[vi] = append(e.Tokens[vi], counter.last)
			e.After[vi] = append(e.After[vi], append(top, list[d:]...))
		}

		judge.logger().Info("re-ranked", "query", qi+1, "of", len(queries))
	}

	return e, nil
}

// spentBy returns the time rr spent in calls, for re-rankers that wait between calls (PacedReranker); -1
// for others.
func spentBy(rr search.Reranker) time.Duration {
	if s, ok := rr.(interface{ Spent() time.Duration }); ok {
		return s.Spent()
	}

	return -1
}

// band returns the band of a 0-based rank.
func band(rank int) int {
	for i, d := range Depths {
		if rank < d {
			return i
		}
	}

	return len(Depths) - 1
}

// bands aggregates the ratings of lists ([query]) per band of ranks.
func (e RerankEffect) bands(lists [][]search.Result) []DepthSummary {
	out := make([]DepthSummary, len(Depths))
	sums, goods := make([]int, len(Depths)), make([]int, len(Depths))
	for qi, list := range lists {
		for i, w := range list {
			b := band(i)
			r, ok := e.Ratings[qi][w.ID]
			if !ok {
				out[b].Unrated++
				continue
			}

			out[b].Rated++
			sums[b] += r
			if r >= 2 {
				goods[b]++
			}
		}
	}

	for b := range out {
		if out[b].Rated > 0 {
			out[b].Mean, out[b].Good = float64(sums[b])/float64(out[b].Rated), float64(goods[b])/float64(out[b].Rated)
		}
	}

	return out
}

// ndcg10 is the nDCG@10 of query qi's list, gain 2^rating−1, against the best order of the same works.
// It returns -1 when no work is rated above 0.
func (e RerankEffect) ndcg10(qi int, list []search.Result) float64 {
	dcg := func(ratings []int) float64 {
		s := 0.0
		for i, r := range ratings[:min(10, len(ratings))] {
			s += (math.Pow(2, float64(r)) - 1) / math.Log2(float64(i+2))
		}

		return s
	}

	ratings := make([]int, len(list))
	for i, w := range list {
		ratings[i] = e.Ratings[qi][w.ID]
	}

	ideal := slices.Clone(ratings)
	slices.SortFunc(ideal, func(a, b int) int { return b - a })
	if best := dcg(ideal); best > 0 {
		return dcg(ratings) / best
	}

	return -1
}

// meanNDCG10 is the mean nDCG@10 of lists ([query]) over the queries with a work rated above 0, and how
// many those are.
func (e RerankEffect) meanNDCG10(lists [][]search.Result) (float64, int) {
	sum, n := 0.0, 0
	for qi, list := range lists {
		if x := e.ndcg10(qi, list); x >= 0 {
			sum += x
			n++
		}
	}

	if n == 0 {
		return 0, 0
	}

	return sum / float64(n), n
}

// moves returns every rated work's rank before and after variant v.
func (e RerankEffect) moves(v int) []move {
	var out []move
	for qi, before := range e.Before {
		after := map[string]int{}
		for i, w := range e.After[v][qi] {
			after[w.ID] = i
		}

		for i, w := range before {
			if r, ok := e.Ratings[qi][w.ID]; ok {
				out = append(out, move{query: qi, before: i, after: after[w.ID], rating: r, work: w})
			}
		}
	}

	return out
}

// transitions counts the moves between bands, [before][after], of the works whose rating keep accepts.
func transitions(moves []move, keep func(rating int) bool) [][]int {
	m := make([][]int, len(Depths))
	for i := range m {
		m[i] = make([]int, len(Depths))
	}

	for _, mv := range moves {
		if keep(mv.rating) {
			m[band(mv.before)][band(mv.after)]++
		}
	}

	return m
}

// WriteMarkdown writes the effect of each variant: ratings per band and nDCG@10 before and after, band
// transitions, the biggest moves, targets and the change per query.
func (e RerankEffect) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	if e.Note != "" {
		b.WriteString("> " + strings.ReplaceAll(e.Note, "\n", " ") + "\n\n")
	}

	e.writeDepths(&b)
	e.writeBands(&b)
	for v := range e.Depths {
		if e.Detailed(v) {
			e.writeTransitions(&b, v)
			e.writeMoves(&b, v)
		}
	}

	e.writeTargets(&b)
	e.writePerQuery(&b)

	_, err := io.WriteString(w, b.String())

	return err
}

func (e RerankEffect) writeBands(b *strings.Builder) {
	fmt.Fprintf(b, "## Ratings by band of ranks\n\n%d queries, top %d each. Mean rating (0–3) / share rated 2 or more. "+
		"nDCG@10 compares the top 10 with the best order of the same %d works (gain 2^rating − 1), over the queries "+
		"with a work rated 1 or more. Re-rank time: mean / slowest per query.\n\n| Order |", len(e.Queries), RecallDepth, RecallDepth)
	for _, n := range bandNames {
		fmt.Fprintf(b, " %s |", n)
	}

	b.WriteString(" nDCG@10 | Re-rank time |\n|---|" + strings.Repeat("---|", len(bandNames)+2) + "\n")
	row := func(name string, lists [][]search.Result, took []time.Duration) {
		fmt.Fprintf(b, "| %s |", name)
		for _, s := range e.bands(lists) {
			fmt.Fprintf(b, " %.2f / %.0f%% |", s.Mean, 100*s.Good)
		}

		ndcg, _ := e.meanNDCG10(lists)
		spent := "–"
		if len(took) > 0 {
			mean, slowest := latency(took)
			spent = fmt.Sprintf("%s / %s", mean, slowest)
		}

		fmt.Fprintf(b, " %.3f | %s |\n", ndcg, spent)
	}

	row("before re-ranking", e.Before, nil)
	for v, d := range e.Depths {
		row(fmt.Sprintf("re-rank top %d", d), e.After[v], e.Took[v])
	}
}

func (e RerankEffect) writeTransitions(b *strings.Builder, v int) {
	moves := e.moves(v)
	fmt.Fprintf(b, "\n## Re-rank top %d: band before → band after\n\nRows: band before re-ranking; columns: band after. "+
		"Each cell: all rated works / rated 2–3 / rated 0.\n\n| Before \\ after |", e.Depths[v])
	for _, n := range bandNames {
		fmt.Fprintf(b, " %s |", n)
	}

	b.WriteString("\n|---|" + strings.Repeat("---|", len(bandNames)) + "\n")
	all := transitions(moves, func(int) bool { return true })
	good := transitions(moves, func(r int) bool { return r >= 2 })
	bad := transitions(moves, func(r int) bool { return r == 0 })
	for i, n := range bandNames {
		fmt.Fprintf(b, "| %s |", n)
		for j := range bandNames {
			fmt.Fprintf(b, " %d / %d / %d |", all[i][j], good[i][j], bad[i][j])
		}

		b.WriteString("\n")
	}

	last := len(Depths) - 1
	fmt.Fprintf(b, "\nInto the top 10 from 11–30: %d rated 2–3, %d rated 0. From 31–100: %d rated 2–3, %d rated 0. "+
		"Pushed out of the top 10: %d rated 2–3, %d rated 0.\n", good[1][0], bad[1][0], good[last][0], bad[last][0],
		good[0][1]+good[0][last], bad[0][1]+bad[0][last])
}

func (e RerankEffect) writeMoves(b *strings.Builder, v int) {
	moves := e.moves(v)
	lists := []struct {
		title string
		keep  func(move) bool
		by    func(move) int
	}{
		{"Rated 2–3 and moved into the top 10 from furthest", func(m move) bool { return m.rating >= 2 && m.after < 10 && m.before >= 10 }, func(m move) int { return m.before }},
		{"Rated 2–3 and pushed out of the top 10", func(m move) bool { return m.rating >= 2 && m.before < 10 && m.after >= 10 }, func(m move) int { return m.after }},
		{"Rated 0 and moved into the top 10", func(m move) bool { return m.rating == 0 && m.after < 10 && m.before >= 10 }, func(m move) int { return m.before }},
	}

	for _, l := range lists {
		var picked []move
		for _, m := range moves {
			if l.keep(m) {
				picked = append(picked, m)
			}
		}

		slices.SortStableFunc(picked, func(x, y move) int { return l.by(y) - l.by(x) })
		fmt.Fprintf(b, "\n**%s** (%d in all; up to 8 shown):\n\n", l.title, len(picked))
		for _, m := range picked[:min(8, len(picked))] {
			fmt.Fprintf(b, "- %d → %d, rated %d: %s, for query %d (%s)\n", m.before+1, m.after+1, m.rating,
				resultCell(Outcome{Results: []search.Result{m.work}}, 0), m.query+1, cell(cutText(e.Queries[m.query].Query, 60)))
		}
	}
}

func (e RerankEffect) writeTargets(b *strings.Builder) {
	variants := e.detailedVariants()
	b.WriteString("\n## Known targets\n\nRank of the query's known book before re-ranking → after; – when not in the top 100.\n\n| # | Target | Before |")
	for _, v := range variants {
		fmt.Fprintf(b, " Re-rank top %d |", e.Depths[v])
	}

	b.WriteString("\n|---|---|---|" + strings.Repeat("---|", len(variants)) + "\n")
	found := make([][2]int, len(variants)+1)
	for qi, q := range e.Queries {
		if len(q.Targets) == 0 {
			continue
		}

		before := rank(e.Before[qi], q.Targets)
		fmt.Fprintf(b, "| %d | %s | %s |", qi+1, cell(q.Title), rankCell(before))
		count := func(i, r int) {
			if r > 0 && r <= 10 {
				found[i][0]++
			}

			if r == 1 {
				found[i][1]++
			}
		}

		count(0, before)
		for i, v := range variants {
			after := rank(e.After[v][qi], q.Targets)
			count(i+1, after)
			fmt.Fprintf(b, " %s |", rankCell(after))
		}

		b.WriteString("\n")
	}

	fmt.Fprintf(b, "| | **In the top 10 / first** | %d / %d |", found[0][0], found[0][1])
	for i := range variants {
		fmt.Fprintf(b, " %d / %d |", found[i+1][0], found[i+1][1])
	}

	b.WriteString("\n")
}

func (e RerankEffect) writePerQuery(b *strings.Builder) {
	variants := e.detailedVariants()
	b.WriteString("\n## Per query\n\nnDCG@10 and works rated 2–3 in the top 10, before → after; – when no work is rated 1 or more.\n\n| # | Kind | Query | Before |")
	for _, v := range variants {
		fmt.Fprintf(b, " Re-rank top %d |", e.Depths[v])
	}

	b.WriteString("\n|---|---|---|---|" + strings.Repeat("---|", len(variants)) + "\n")
	good10 := func(qi int, list []search.Result) int {
		n := 0
		for _, w := range list[:min(10, len(list))] {
			if e.Ratings[qi][w.ID] >= 2 {
				n++
			}
		}

		return n
	}

	ndcg := func(qi int, list []search.Result) string {
		if x := e.ndcg10(qi, list); x >= 0 {
			return fmt.Sprintf("%.2f", x)
		}

		return "–"
	}

	for qi, q := range e.Queries {
		fmt.Fprintf(b, "| %d | %s | %s | %s, %d |", qi+1, cell(q.Kind), cell(cutText(q.Query, 60)),
			ndcg(qi, e.Before[qi]), good10(qi, e.Before[qi]))
		for _, v := range variants {
			fmt.Fprintf(b, " %s, %d |", ndcg(qi, e.After[v][qi]), good10(qi, e.After[v][qi]))
		}

		b.WriteString("\n")
	}
}

// Detailed reports whether variant v gets the detailed sections (transitions, moves, targets, per query,
// charts): re-ranking the top 30, as the search does, or all 100.
func (e RerankEffect) Detailed(v int) bool {
	return e.Depths[v] == search.DefaultCandidates || e.Depths[v] == RecallDepth
}

func (e RerankEffect) detailedVariants() []int {
	var out []int
	for v := range e.Depths {
		if e.Detailed(v) {
			out = append(out, v)
		}
	}

	return out
}

// writeDepths writes, for each depth re-ranked, the quality of the top 10 and the tokens billed.
func (e RerankEffect) writeDepths(b *strings.Builder) {
	b.WriteString("## By re-rank depth\n\nRe-ranking the top N of the 100 retrieved works. Top 10: mean rating / share rated 2–3. " +
		"Targets: known books in the top 10 / first, of the queries with one. Tokens: estimated tokens billed per search " +
		"(4 bytes per token; the query's tokens count once per card).\n\n| Re-ranked | nDCG@10 | Top 10 | Targets | Tokens |\n|---|---|---|---|---|\n")
	targets := func(lists [][]search.Result) (top10, first int) {
		for qi, q := range e.Queries {
			if r := rank(lists[qi], q.Targets); r > 0 && r <= 10 {
				top10++
				if r == 1 {
					first++
				}
			}
		}

		return top10, first
	}

	row := func(name string, lists [][]search.Result, tokens []int) {
		ndcg, _ := e.meanNDCG10(lists)
		top := e.bands(lists)[0]
		t10, first := targets(lists)
		tok := "–"
		if len(tokens) > 0 {
			sum := 0
			for _, t := range tokens {
				sum += t
			}

			tok = fmt.Sprintf("%d", sum/len(tokens))
		}

		fmt.Fprintf(b, "| %s | %.3f | %.2f / %.0f%% | %d / %d | %s |\n", name, ndcg, top.Mean, 100*top.Good, t10, first, tok)
	}

	row("none", e.Before, nil)
	order := make([]int, len(e.Depths))
	for i := range order {
		order[i] = i
	}

	slices.SortFunc(order, func(x, y int) int { return e.Depths[x] - e.Depths[y] })
	for _, v := range order {
		row(fmt.Sprintf("top %d", e.Depths[v]), e.After[v], e.Tokens[v])
	}

	b.WriteString("\n")
}
