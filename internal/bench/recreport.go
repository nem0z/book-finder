package bench

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

// lengthOrder is the order lengths are reported in.
var lengthOrder = []string{"S", "M", "L", "XL"}

// WriteMarkdown writes the accuracy of the evaluated method (see writeAccuracy), then the relevance of each
// method's top 10, then by band of ranks, category, query length
// and dimension, examples, flagged queries and an appendix per query.
func (r *Recommendation) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	if r.Note != "" {
		b.WriteString("> " + strings.ReplaceAll(r.Note, "\n", " ") + "\n\n")
	}

	r.writeAccuracy(&b)
	if r.Pool > 0 {
		b.WriteString("\n# Appendix: every method\n\n")
	}

	r.writeTop10(&b)
	r.writeRecBands(&b)
	r.writeGroups(&b, "category", func(q Query) string { return q.Category }, nil)
	r.writeGroups(&b, "query length", func(q Query) string { return q.Length }, lengthOrder)
	r.writeDimensions(&b)
	r.writeExamples(&b)
	r.writeFlagged(&b)
	r.writeRecPerQuery(&b)

	_, err := io.WriteString(w, b.String())

	return err
}

func all(int) bool { return true }

func (r *Recommendation) writeTop10(b *strings.Builder) {
	flagged := 0
	for _, q := range r.Queries {
		if q.Flag != "" {
			flagged++
		}
	}

	fmt.Fprintf(b, "## Top 10\n\n%d queries. Each method's top 100 was rated by an LLM from 0 (not a recommendation for "+
		"this reader) to 3 (excellent). Mean: mean rating of the top 10. ≥2: share of the top 10 rated 2 or 3. nDCG@10: "+
		"the top 10 against the best 10 of all the works rated for the query (gain 2^rating − 1); the last column leaves "+
		"out the %d flagged queries. Times: mean / slowest per query.\n\n", len(r.Queries), flagged)
	b.WriteString("| Method | Mean | ≥2 | nDCG@10 | nDCG@10, unflagged | Retrieval | Re-rank |\n|---|---|---|---|---|---|---|\n")
	for m, name := range r.Methods {
		mean, good, _ := r.relevance(m, 0, 10, all)
		rt, rs := latency(r.Retrieval[m])
		rerank := "–"
		if r.Rerank[m] != nil {
			km, ks := latency(r.Rerank[m])
			rerank = fmt.Sprintf("%s / %s", km, ks)
		}

		fmt.Fprintf(b, "| %s | %.2f | %.0f%% | %.3f | %.3f | %s / %s | %s |\n", cell(name), mean, 100*good,
			r.meanNDCG10(m, all), r.meanNDCG10(m, func(qi int) bool { return r.Queries[qi].Flag == "" }), rt, rs, rerank)
	}

	fmt.Fprintf(b, "\nPer query, nDCG@10 against %s (better / worse / same):", r.Methods[r.Baseline])
	for m, name := range r.Methods {
		if m == r.Baseline {
			continue
		}

		better, worse, same := 0, 0, 0
		for qi := range r.Queries {
			x, y := r.ndcg10(m, qi), r.ndcg10(r.Baseline, qi)
			switch {
			case x > y:
				better++
			case x < y:
				worse++
			default:
				same++
			}
		}

		fmt.Fprintf(b, " %s %d / %d / %d;", name, better, worse, same)
	}

	b.WriteString("\n")
}

func (r *Recommendation) writeRecBands(b *strings.Builder) {
	b.WriteString("\n## By band of ranks\n\nMean rating / share rated 2–3.\n\n| Method |")
	for _, n := range bandNames {
		fmt.Fprintf(b, " %s |", n)
	}

	b.WriteString("\n|---|" + strings.Repeat("---|", len(bandNames)) + "\n")
	for m, name := range r.Methods {
		fmt.Fprintf(b, "| %s |", cell(name))
		from := 0
		for _, d := range Depths {
			mean, good, _ := r.relevance(m, from, d, all)
			fmt.Fprintf(b, " %.2f / %.0f%% |", mean, 100*good)
			from = d
		}

		b.WriteString("\n")
	}
}

// writeGroups writes the top 10 of each method per group of queries: nDCG@10 and share rated 2–3.
func (r *Recommendation) writeGroups(b *strings.Builder, title string, group func(Query) string, order []string) {
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

	fmt.Fprintf(b, "\n## By %s\n\nTop 10: nDCG@10 · share rated 2–3.\n\n| %s | Queries |", title, strings.ToUpper(title[:1])+title[1:])
	for _, name := range r.Methods {
		fmt.Fprintf(b, " %s |", cell(name))
	}

	b.WriteString("\n|---|---|" + strings.Repeat("---|", len(r.Methods)) + "\n")
	for _, g := range names {
		keep := func(qi int) bool { return group(r.Queries[qi]) == g }
		fmt.Fprintf(b, "| %s | %d |", cell(g), counts[g])
		for m := range r.Methods {
			_, good, _ := r.relevance(m, 0, 10, keep)
			fmt.Fprintf(b, " %.2f · %.0f%% |", r.meanNDCG10(m, keep), 100*good)
		}

		b.WriteString("\n")
	}
}

func (r *Recommendation) writeDimensions(b *strings.Builder) {
	b.WriteString("\n## By dimension\n\nFor the top 10 results, on each dimension the judge found the query asks about: mean score " +
		"(0 misses it, 1 partly fits, 2 fits) · share scored 0. Results counts in brackets.\n\n| Dimension |")
	for _, name := range r.Methods {
		fmt.Fprintf(b, " %s |", cell(name))
	}

	b.WriteString("\n|---|" + strings.Repeat("---|", len(r.Methods)) + "\n")
	for _, d := range RecommendationDimensions {
		fmt.Fprintf(b, "| %s |", d)
		for m := range r.Methods {
			mean, zero, n := r.dimension(m, d)
			fmt.Fprintf(b, " %.2f · %.0f%% (%d) |", mean, 100*zero, n)
		}

		b.WriteString("\n")
	}
}

// writeExamples shows the top 5 of the baseline and the proposed method for the queries where the proposed
// method gains and loses most.
func (r *Recommendation) writeExamples(b *strings.Builder) {
	if r.Proposed == r.Baseline {
		return
	}

	type diff struct {
		qi int
		d  float64
	}

	var diffs []diff
	for qi := range r.Queries {
		x, y := r.ndcg10(r.Proposed, qi), r.ndcg10(r.Baseline, qi)
		if x >= 0 && y >= 0 {
			diffs = append(diffs, diff{qi, x - y})
		}
	}

	slices.SortStableFunc(diffs, func(a, b diff) int {
		switch {
		case a.d > b.d:
			return -1
		case a.d < b.d:
			return 1
		default:
			return 0
		}
	})

	var picked []diff
	for i := range min(2, len(diffs)) {
		picked = append(picked, diffs[i])
	}

	for i := max(len(picked), len(diffs)-2); i < len(diffs); i++ {
		picked = append(picked, diffs[i])
	}

	fmt.Fprintf(b, "\n## Examples\n\nThe queries where %s gains and loses most against %s: their top 5, with the rating.\n",
		r.Methods[r.Proposed], r.Methods[r.Baseline])
	for _, p := range picked {
		q := r.Queries[p.qi]
		fmt.Fprintf(b, "\n**%d. %s** (%s, %s): nDCG@10 %.2f → %.2f\n\n| # | %s | %s |\n|---|---|---|\n", p.qi+1, cell(q.Query),
			q.Category, q.Length, r.ndcg10(r.Baseline, p.qi), r.ndcg10(r.Proposed, p.qi), cell(r.Methods[r.Baseline]), cell(r.Methods[r.Proposed]))
		for k := range 5 {
			fmt.Fprintf(b, "| %d | %s | %s |\n", k+1, r.exampleCell(r.Baseline, p.qi, k), r.exampleCell(r.Proposed, p.qi, k))
		}
	}
}

func (r *Recommendation) exampleCell(m, qi, k int) string {
	list := r.Orders[m][qi]
	if k >= len(list) {
		return ""
	}

	s := resultCell(Outcome{Results: list[k : k+1]}, 0)
	if x, ok := r.rating(qi, list[k]); ok {
		s += fmt.Sprintf(": **%d**", x)
	}

	return s
}

func (r *Recommendation) writeFlagged(b *strings.Builder) {
	var flagged []int
	for qi, q := range r.Queries {
		if q.Flag != "" {
			flagged = append(flagged, qi)
		}
	}

	if len(flagged) == 0 {
		return
	}

	b.WriteString("\n## Flagged queries\n\nKnown catalogue limits that may sink these queries whatever the search. nDCG@10 · share of the top 10 rated 2–3.\n\n| # | Query | Flag |")
	for _, name := range r.Methods {
		fmt.Fprintf(b, " %s |", cell(name))
	}

	b.WriteString("\n|---|---|---|" + strings.Repeat("---|", len(r.Methods)) + "\n")
	for _, qi := range flagged {
		q := r.Queries[qi]
		fmt.Fprintf(b, "| %d | %s | %s |", qi+1, cell(cutText(q.Query, 60)), cell(q.Flag))
		keep := func(i int) bool { return i == qi }
		for m := range r.Methods {
			_, good, _ := r.relevance(m, 0, 10, keep)
			fmt.Fprintf(b, " %s · %.0f%% |", ndcgCell(r.ndcg10(m, qi)), 100*good)
		}

		b.WriteString("\n")
	}
}

func (r *Recommendation) writeRecPerQuery(b *strings.Builder) {
	b.WriteString("\n## Per query\n\nnDCG@10 · mean rating of the top 10; – when no work is rated 1 or more. * flagged.\n\n| # | Category | Length | Query |")
	for _, name := range r.Methods {
		fmt.Fprintf(b, " %s |", cell(name))
	}

	b.WriteString("\n|---|---|---|---|" + strings.Repeat("---|", len(r.Methods)) + "\n")
	for qi, q := range r.Queries {
		mark := ""
		if q.Flag != "" {
			mark = " *"
		}

		fmt.Fprintf(b, "| %d%s | %s | %s | %s |", qi+1, mark, cell(q.Category), q.Length, cell(cutText(q.Query, 60)))
		keep := func(i int) bool { return i == qi }
		for m := range r.Methods {
			mean, _, _ := r.relevance(m, 0, 10, keep)
			fmt.Fprintf(b, " %s · %.1f |", ndcgCell(r.ndcg10(m, qi)), mean)
		}

		b.WriteString("\n")
	}
}

func ndcgCell(x float64) string {
	if x < 0 {
		return "–"
	}

	return fmt.Sprintf("%.2f", x)
}
