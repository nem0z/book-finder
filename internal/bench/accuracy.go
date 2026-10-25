package bench

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// worstQueries is how many queries the failure analysis covers.
const worstQueries = 10

// queryStats is how one query fares with the evaluated method.
type queryStats struct {
	qi                  int
	poolGood, top10Good int     // works rated 2–3 in the pool and in the final top 10
	elsewhereGood       int     // works rated 2–3 that some method found, outside the pool
	top10Mean           float64 // mean rating of the final top 10
}

// possible is how many top-10 slots the pool could fill with works rated 2–3.
func (s queryStats) possible() int { return min(10, s.poolGood) }

// cause says why a query's top 10 is poor: the re-ranking leaves good works of the pool out, the pool misses
// good works other searches find, or no search finds them, which suggests a gap in the catalogue.
func (s queryStats) cause() string {
	switch {
	case s.possible()-s.top10Good >= 3:
		return fmt.Sprintf("ranking: the pool has %d works rated 2–3, the top 10 only %d", s.poolGood, s.top10Good)
	case s.poolGood < 10 && s.elsewhereGood >= 3:
		return fmt.Sprintf("retrieval: %d works rated 2–3 found outside the pool (lower keyword-vector ranks or the description-vector top 100), %d in it",
			s.elsewhereGood, s.poolGood)
	default:
		return fmt.Sprintf("catalogue gap? %d works rated 2–3 in the pool, %d found elsewhere", s.poolGood, s.elsewhereGood)
	}
}

// stats returns how query qi fares with the evaluated method: its final top 10 and its pool, the first Pool
// results of the method it re-ranks (or its own when it doesn't re-rank).
func (r *Recommendation) stats(qi int) queryStats {
	base := r.Evaluated
	if r.EvaluatedBase >= 0 {
		base = r.EvaluatedBase
	}

	s := queryStats{qi: qi}
	pool := map[string]bool{}
	list := r.Orders[base][qi]
	for _, w := range list[:min(r.Pool, len(list))] {
		pool[w.ID] = true
		if x, ok := r.rating(qi, w); ok && x >= 2 {
			s.poolGood++
		}
	}

	for id, jd := range r.Judgments[qi] {
		if jd.Rating >= 2 && !pool[id] {
			s.elsewhereGood++
		}
	}

	final := r.Orders[r.Evaluated][qi]
	sum, n := 0, 0
	for _, w := range final[:min(10, len(final))] {
		if x, ok := r.rating(qi, w); ok {
			sum += x
			n++
			if x >= 2 {
				s.top10Good++
			}
		}
	}

	if n > 0 {
		s.top10Mean = float64(sum) / float64(n)
	}

	return s
}

// writeAccuracy writes the lead of the report: how relevant the evaluated method's pool and final top 10
// are, how well the re-ranking uses the pool, by category, length and dimension, and the worst queries.
func (r *Recommendation) writeAccuracy(b *strings.Builder) {
	if r.Pool <= 0 {
		return
	}

	e := r.Methods[r.Evaluated]
	base := r.Evaluated
	if r.EvaluatedBase >= 0 {
		base = r.EvaluatedBase
	}

	stats := make([]queryStats, len(r.Queries))
	for qi := range r.Queries {
		stats[qi] = r.stats(qi)
	}

	fmt.Fprintf(b, "# Accuracy of %s\n\n%d queries asking for a kind of book. An LLM rated each result from 0 (not a "+
		"recommendation for this reader) to 3 (excellent); relevant means rated 2 or 3.\n\n", e, len(r.Queries))

	// Stage 1: the pool.
	poolMean, poolGood, _ := r.relevance(base, 0, r.Pool, all)
	full, sumGood := 0, 0
	for _, s := range stats {
		sumGood += s.poolGood
		if s.poolGood >= 10 {
			full++
		}
	}

	fmt.Fprintf(b, "## Retrieval: the top %d (%s)\n\nThe candidates the re-ranker orders; their order doesn't matter here.\n\n"+
		"- %.1f relevant works per query on average (%.0f%% of the pool), mean rating %.2f.\n"+
		"- %d of %d queries have at least 10 relevant works in the pool, enough to fill a top 10.\n",
		r.Pool, r.Methods[base], float64(sumGood)/float64(len(r.Queries)), 100*poolGood, poolMean, full, len(r.Queries))

	// Stage 2: the final top 10.
	mean, good, _ := r.relevance(r.Evaluated, 0, 10, all)
	three, zero, rated := 0, 0, 0
	five, eight, possible, filled := 0, 0, 0, 0
	for qi, s := range stats {
		for _, w := range r.Orders[r.Evaluated][qi][:min(10, len(r.Orders[r.Evaluated][qi]))] {
			if x, ok := r.rating(qi, w); ok {
				rated++
				if x == 3 {
					three++
				}

				if x == 0 {
					zero++
				}
			}
		}

		if s.top10Good >= 5 {
			five++
		}

		if s.top10Good >= 8 {
			eight++
		}

		possible += s.possible()
		filled += min(s.top10Good, s.possible())
	}

	n := float64(len(r.Queries))
	fmt.Fprintf(b, "\n## Final top 10\n\n| Mean rating | Rated 2–3 | Rated 3 | Rated 0 | nDCG@10 | Queries with ≥5 relevant | ≥8 relevant |\n"+
		"|---|---|---|---|---|---|---|\n| %.2f | %.0f%% | %.0f%% | %.0f%% | %.3f | %d of %d | %d of %d |\n\n",
		mean, 100*good, 100*float64(three)/float64(rated), 100*float64(zero)/float64(rated), r.meanNDCG10(r.Evaluated, all),
		five, len(r.Queries), eight, len(r.Queries))
	fmt.Fprintf(b, "Use of the pool: the top 10 holds %.1f of the %.1f relevant works the pool could put there, on average per "+
		"query (%.0f%%); %.1f slots per query go to less relevant works while relevant ones stay below rank 10.\n",
		float64(filled)/n, float64(possible)/n, 100*float64(filled)/float64(max(possible, 1)), float64(possible-filled)/n)

	if r.EvaluatedBase >= 0 {
		bm, bg, _ := r.relevance(r.EvaluatedBase, 0, 10, all)
		fmt.Fprintf(b, "\nRe-ranking: the top 10 goes from mean %.2f / %.0f%% rated 2–3 / nDCG@10 %.3f in retrieval order to "+
			"%.2f / %.0f%% / %.3f.\n", bm, 100*bg, r.meanNDCG10(r.EvaluatedBase, all), mean, 100*good, r.meanNDCG10(r.Evaluated, all))
	}

	r.writeAccuracyGroups(b, "category", stats, func(q Query) string { return q.Category }, nil)
	r.writeAccuracyGroups(b, "query length", stats, func(q Query) string { return q.Length }, lengthOrder)
	r.writeAccuracyDimensions(b, base)
	r.writeFailures(b, stats)
}

func (r *Recommendation) writeAccuracyGroups(b *strings.Builder, title string, stats []queryStats, group func(Query) string, order []string) {
	counts := map[string]int{}
	var names []string
	for _, q := range r.Queries {
		g := group(q)
		if counts[g] == 0 {
			names = append(names, g)
		}

		counts[g]++
	}

	if order != nil {
		sort.SliceStable(names, func(i, j int) bool { return slices.Index(order, names[i]) < slices.Index(order, names[j]) })
	}

	fmt.Fprintf(b, "\n## By %s\n\nPool: relevant works per query · queries with ≥10. Top 10: mean rating · rated 2–3 · nDCG@10.\n\n"+
		"| %s | Queries | Pool | Top 10 |\n|---|---|---|---|\n", title, strings.ToUpper(title[:1])+title[1:])
	for _, g := range names {
		keep := func(qi int) bool { return group(r.Queries[qi]) == g }
		sum, full := 0, 0
		for _, s := range stats {
			if keep(s.qi) {
				sum += s.poolGood
				if s.poolGood >= 10 {
					full++
				}
			}
		}

		mean, good, _ := r.relevance(r.Evaluated, 0, 10, keep)
		fmt.Fprintf(b, "| %s | %d | %.1f · %d | %.2f · %.0f%% · %.2f |\n", cell(g), counts[g], float64(sum)/float64(counts[g]), full,
			mean, 100*good, r.meanNDCG10(r.Evaluated, keep))
	}
}

func (r *Recommendation) writeAccuracyDimensions(b *strings.Builder, base int) {
	b.WriteString("\n## By dimension\n\nOn each dimension the judge found the query asks about: mean score (0 misses it, 1 partly " +
		"fits, 2 fits) · share scored 0, with the number of results.\n\n| Dimension | Pool | Top 10 |\n|---|---|---|\n")
	for _, d := range RecommendationDimensions {
		pm, pz, pn := r.dimensionAt(base, d, r.Pool)
		tm, tz, tn := r.dimension(r.Evaluated, d)
		fmt.Fprintf(b, "| %s | %.2f · %.0f%% (%d) | %.2f · %.0f%% (%d) |\n", d, pm, 100*pz, pn, tm, 100*tz, tn)
	}
}

// writeFailures writes the queries with the fewest relevant works in the top 10, their top 5 and a cause.
func (r *Recommendation) writeFailures(b *strings.Builder, stats []queryStats) {
	worst := slices.Clone(stats)
	slices.SortStableFunc(worst, func(x, y queryStats) int {
		if x.top10Good != y.top10Good {
			return x.top10Good - y.top10Good
		}

		switch {
		case x.top10Mean < y.top10Mean:
			return -1
		case x.top10Mean > y.top10Mean:
			return 1
		default:
			return 0
		}
	})

	causes := map[string]int{}
	for _, s := range worst[:min(worstQueries, len(worst))] {
		causes[strings.SplitN(s.cause(), ":", 2)[0]]++
	}

	var summary []string
	for _, c := range []string{"ranking", "retrieval", "catalogue gap?"} {
		if causes[c] > 0 {
			summary = append(summary, fmt.Sprintf("%s %d", c, causes[c]))
		}
	}

	fmt.Fprintf(b, "\n## The %d worst queries\n\nFewest relevant works in the top 10. Cause: ranking when the pool has at least 3 "+
		"more relevant works than the top 10 shows; retrieval when the pool lacks them but other searches found at least 3; "+
		"otherwise a possible catalogue gap. Causes: %s. Each book shows its rating and the weakest dimension asked about.\n",
		min(worstQueries, len(worst)), strings.Join(summary, ", "))
	for _, s := range worst[:min(worstQueries, len(worst))] {
		q := r.Queries[s.qi]
		flag := ""
		if q.Flag != "" {
			flag = " Flagged: " + q.Flag + "."
		}

		fmt.Fprintf(b, "\n**%d. %s** (%s, %s): %d relevant in the top 10, mean %.2f. Most missed: %s. **Cause: %s.**%s\n\n",
			s.qi+1, cell(q.Query), q.Category, q.Length, s.top10Good, s.top10Mean, r.mostMissed(s.qi), s.cause(), flag)
		list := r.Orders[r.Evaluated][s.qi]
		for k, w := range list[:min(5, len(list))] {
			jd, ok := r.Judgments[s.qi][w.ID]
			if !ok {
				fmt.Fprintf(b, "%d. %s: unrated\n", k+1, resultCell(Outcome{Results: list[k : k+1]}, 0))
				continue
			}

			fmt.Fprintf(b, "%d. %s: **%d**%s, %s\n", k+1, resultCell(Outcome{Results: list[k : k+1]}, 0), jd.Rating, weakest(jd), cell(jd.Reason))
		}
	}
}

// weakest names the lowest-scored dimension of a judgment, among those asked about, when it isn't a full fit.
func weakest(jd Judgment) string {
	best, score := "", MaxDimension
	for _, d := range RecommendationDimensions {
		if s, ok := jd.Dimensions[d]; ok && s != NotApplicable && s < score {
			best, score = d, s
		}
	}

	if best == "" {
		return ""
	}

	return fmt.Sprintf(" (%s %d)", best, score)
}

// mostMissed names the dimension most often scored 0 in query qi's final top 10.
func (r *Recommendation) mostMissed(qi int) string {
	zeros := map[string]int{}
	for _, w := range r.Orders[r.Evaluated][qi][:min(10, len(r.Orders[r.Evaluated][qi]))] {
		for d, s := range r.Judgments[qi][w.ID].Dimensions {
			if s == 0 {
				zeros[d]++
			}
		}
	}

	best, n := "none", 0
	for _, d := range RecommendationDimensions {
		if zeros[d] > n {
			best, n = d, zeros[d]
		}
	}

	if n == 0 {
		return best
	}

	return fmt.Sprintf("%s (%d of 10 miss it)", best, n)
}
