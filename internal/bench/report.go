package bench

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nem0z/book-finder/internal/search"
)

// worstQueries is how many queries the failure analysis covers.
const worstQueries = 10

// lengthOrder is the order lengths are reported in.
var lengthOrder = []string{"S", "M", "L", "XL"}

// WriteMarkdown writes the report: the relevance of the pool and of the final top 10, how well re-ranking
// uses the pool, by category, query length and dimension, the worst queries with a likely cause, flagged
// queries, every query and the times.
func (ev *Eval) WriteMarkdown(w io.Writer) error {
	stats := make([]queryStats, len(ev.Queries))
	for qi := range ev.Queries {
		stats[qi] = ev.stats(qi)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Accuracy of the search\n\n")
	if ev.Note != "" {
		b.WriteString("> " + strings.ReplaceAll(ev.Note, "\n", " ") + "\n\n")
	}

	fmt.Fprintf(&b, "%d queries asking for a kind of book. An LLM rated each retrieved work from 0 (not a recommendation "+
		"for this reader) to 3 (excellent); relevant means rated 2 or 3.\n", len(ev.Queries))
	ev.writePool(&b, stats)
	ev.writeTop10(&b, stats)
	ev.writeGroups(&b, "category", stats, func(q Query) string { return q.Category }, nil)
	ev.writeGroups(&b, "query length", stats, func(q Query) string { return q.Length }, lengthOrder)
	ev.writeDimensions(&b)
	ev.writeFailures(&b, stats)
	ev.writeFlagged(&b, stats)
	ev.writePerQuery(&b, stats)
	ev.writeTimes(&b)

	_, err := io.WriteString(w, b.String())

	return err
}

func (ev *Eval) writePool(b *strings.Builder, stats []queryStats) {
	mean, good, _, _, _ := ev.ratings(ev.retrieved, 0, ev.Pool, all)
	sum, full := 0, 0
	for _, s := range stats {
		sum += s.poolGood
		if s.poolGood >= 10 {
			full++
		}
	}

	fmt.Fprintf(b, "\n## Retrieval: the top %d\n\nThe candidates the re-ranker orders; their order doesn't matter here.\n\n"+
		"- %.1f relevant works per query on average (%.0f%% of the pool), mean rating %.2f.\n"+
		"- %d of %d queries have at least 10 relevant works in the pool, enough to fill a top 10.\n",
		ev.Pool, float64(sum)/float64(len(ev.Queries)), 100*good, mean, full, len(ev.Queries))
}

func (ev *Eval) writeTop10(b *strings.Builder, stats []queryStats) {
	mean, good, three, zero, _ := ev.ratings(ev.final, 0, 10, all)
	five, eight, possible, filled := 0, 0, 0, 0
	for _, s := range stats {
		if s.top10Good >= 5 {
			five++
		}

		if s.top10Good >= 8 {
			eight++
		}

		possible += s.possible()
		filled += min(s.top10Good, s.possible())
	}

	n := len(ev.Queries)
	title := "Final top 10"
	if ev.Final == nil {
		title = "Top 10, without re-ranking"
	}

	fmt.Fprintf(b, "\n## %s\n\n| Mean rating | Rated 2–3 | Rated 3 | Rated 0 | nDCG@10 | Queries with ≥5 relevant | ≥8 relevant |\n"+
		"|---|---|---|---|---|---|---|\n| %.2f | %.0f%% | %.0f%% | %.0f%% | %.3f | %d of %d | %d of %d |\n",
		title, mean, 100*good, 100*three, 100*zero, ev.meanNDCG10(ev.final, all), five, n, eight, n)
	if ev.Final == nil {
		return
	}

	fmt.Fprintf(b, "\nUse of the pool: the top 10 holds %.1f of the %.1f relevant works the pool could put there, on average per "+
		"query (%.0f%%); %.1f slots per query go to less relevant works while relevant ones stay below rank 10.\n",
		float64(filled)/float64(n), float64(possible)/float64(n), 100*float64(filled)/float64(max(possible, 1)),
		float64(possible-filled)/float64(n))

	bm, bg, _, _, _ := ev.ratings(ev.retrieved, 0, 10, all)
	fmt.Fprintf(b, "\nRe-ranking: the top 10 goes from mean %.2f / %.0f%% rated 2–3 / nDCG@10 %.3f in retrieval order to "+
		"%.2f / %.0f%% / %.3f.\n", bm, 100*bg, ev.meanNDCG10(ev.retrieved, all), mean, 100*good, ev.meanNDCG10(ev.final, all))
}

func (ev *Eval) writeGroups(b *strings.Builder, title string, stats []queryStats, group func(Query) string, order []string) {
	counts := map[string]int{}
	var names []string
	for _, q := range ev.Queries {
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
		keep := func(qi int) bool { return group(ev.Queries[qi]) == g }
		sum, full := 0, 0
		for _, s := range stats {
			if keep(s.qi) {
				sum += s.poolGood
				if s.poolGood >= 10 {
					full++
				}
			}
		}

		mean, good, _, _, _ := ev.ratings(ev.final, 0, 10, keep)
		fmt.Fprintf(b, "| %s | %d | %.1f · %d | %.2f · %.0f%% · %.2f |\n", cell(g), counts[g], float64(sum)/float64(counts[g]), full,
			mean, 100*good, ev.meanNDCG10(ev.final, keep))
	}
}

func (ev *Eval) writeDimensions(b *strings.Builder) {
	b.WriteString("\n## By dimension\n\nOn each dimension the judge found the query asks about: mean score (0 misses it, 1 partly " +
		"fits, 2 fits) · share scored 0, with the number of works.\n\n| Dimension | Pool | Top 10 |\n|---|---|---|\n")
	for _, d := range Dimensions {
		pm, pz, pn := ev.dimension(ev.retrieved, d, ev.Pool)
		tm, tz, tn := ev.dimension(ev.final, d, 10)
		fmt.Fprintf(b, "| %s | %.2f · %.0f%% (%d) | %.2f · %.0f%% (%d) |\n", d, pm, 100*pz, pn, tm, 100*tz, tn)
	}
}

// writeFailures writes the queries with the fewest relevant works in the top 10, their top 5 and a cause.
func (ev *Eval) writeFailures(b *strings.Builder, stats []queryStats) {
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
	worst = worst[:min(worstQueries, len(worst))]

	causes := map[string]int{}
	for _, s := range worst {
		causes[s.causeKind()]++
	}

	var summary []string
	for _, c := range []string{"ranking", "retrieval", "catalogue gap?"} {
		if causes[c] > 0 {
			summary = append(summary, fmt.Sprintf("%s %d", c, causes[c]))
		}
	}

	fmt.Fprintf(b, "\n## The %d worst queries\n\nFewest relevant works in the top 10. Cause: ranking when the pool has at least 3 "+
		"more relevant works than the top 10 shows; retrieval when the pool lacks them but at least 3 are retrieved below it; "+
		"otherwise a possible catalogue gap, to check by hand. Causes: %s. Each book shows its rating and the weakest "+
		"dimension asked about.\n", len(worst), strings.Join(summary, ", "))
	for _, s := range worst {
		q := ev.Queries[s.qi]
		flag := ""
		if q.Flag != "" {
			flag = " Flagged: " + q.Flag + "."
		}

		fmt.Fprintf(b, "\n**%d. %s** (%s, %s): %d relevant in the top 10, mean %.2f. Most missed: %s. **Cause: %s.**%s\n\n",
			s.qi+1, cell(q.Query), q.Category, q.Length, s.top10Good, s.top10Mean, ev.mostMissed(s.qi), s.cause(), flag)
		list := ev.final(s.qi)
		for k, w := range list[:min(5, len(list))] {
			jd, ok := ev.Judgments[s.qi][w.ID]
			if !ok {
				fmt.Fprintf(b, "%d. %s: unrated\n", k+1, workCell(w))
				continue
			}

			fmt.Fprintf(b, "%d. %s: **%d**%s, %s\n", k+1, workCell(w), jd.Rating, weakest(jd), cell(jd.Reason))
		}
	}
}

func (ev *Eval) writeFlagged(b *strings.Builder, stats []queryStats) {
	var flagged []int
	for qi, q := range ev.Queries {
		if q.Flag != "" {
			flagged = append(flagged, qi)
		}
	}

	if len(flagged) == 0 {
		return
	}

	b.WriteString("\n## Flagged queries\n\nKnown catalogue limits that may sink these queries whatever the search.\n\n" +
		"| # | Query | Flag | Relevant in the pool | In the top 10 | nDCG@10 |\n|---|---|---|---|---|---|\n")
	for _, qi := range flagged {
		q := ev.Queries[qi]
		fmt.Fprintf(b, "| %d | %s | %s | %d | %d | %s |\n", qi+1, cell(cutText(q.Query, 60)), cell(q.Flag), stats[qi].poolGood,
			stats[qi].top10Good, ndcgCell(ev.ndcg10(ev.final, qi)))
	}
}

func (ev *Eval) writePerQuery(b *strings.Builder, stats []queryStats) {
	b.WriteString("\n## Per query\n\n* flagged.\n\n| # | Category | Length | Query | Relevant in the pool | In the top 10 | " +
		"Top 10 mean | nDCG@10 |\n|---|---|---|---|---|---|---|---|\n")
	for qi, q := range ev.Queries {
		mark := ""
		if q.Flag != "" {
			mark = " *"
		}

		s := stats[qi]
		fmt.Fprintf(b, "| %d%s | %s | %s | %s | %d | %d | %.1f | %s |\n", qi+1, mark, cell(q.Category), q.Length,
			cell(cutText(q.Query, 60)), s.poolGood, s.top10Good, s.top10Mean, ndcgCell(ev.ndcg10(ev.final, qi)))
	}
}

func (ev *Eval) writeTimes(b *strings.Builder) {
	rm, rs := latency(ev.Retrieval)
	fmt.Fprintf(b, "\n## Times\n\nPer query, mean / slowest. Retrieval: %s / %s.", rm, rs)
	if ev.Final != nil {
		km, ks := latency(ev.Rerank)
		if ks == 0 {
			b.WriteString(" Re-ranking: every score came from the cache.")
		} else {
			fmt.Fprintf(b, " Re-ranking the pool: %s / %s (calls only, without waits for a rate limit).", km, ks)
		}
	}

	b.WriteString("\n")
}

// weakest names the lowest-scored dimension of a judgment, among those asked about, when it isn't a full fit.
func weakest(jd Judgment) string {
	best, score := "", MaxDimension
	for _, d := range Dimensions {
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
func (ev *Eval) mostMissed(qi int) string {
	zeros := map[string]int{}
	list := ev.final(qi)
	for _, w := range list[:min(10, len(list))] {
		for d, s := range ev.Judgments[qi][w.ID].Dimensions {
			if s == 0 {
				zeros[d]++
			}
		}
	}

	best, n := "none", 0
	for _, d := range Dimensions {
		if zeros[d] > n {
			best, n = d, zeros[d]
		}
	}

	if n == 0 {
		return best
	}

	return fmt.Sprintf("%s (%d of 10 miss it)", best, n)
}

// latency returns the mean and the slowest of ds, rounded.
func latency(ds []time.Duration) (mean, slowest time.Duration) {
	if len(ds) == 0 {
		return 0, 0
	}

	var sum time.Duration
	for _, d := range ds {
		sum += d
		slowest = max(slowest, d)
	}

	return round(sum / time.Duration(len(ds))), round(slowest)
}

func round(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}

	return d.Round(10 * time.Millisecond)
}

func workCell(w search.Result) string {
	s := w.Title
	if len(w.Authors) > 0 {
		s += ", " + w.Authors[0]
	}

	if w.Year > 0 {
		s += " (" + strconv.Itoa(w.Year) + ")"
	}

	return cell(s)
}

func ndcgCell(x float64) string {
	if x < 0 {
		return "–"
	}

	return fmt.Sprintf("%.2f", x)
}

// cell makes s safe inside a Markdown table cell.
func cell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}
