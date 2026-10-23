package bench

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nem0z/book-finder/internal/search"
)

// Depths are the list lengths ratings are summarised at.
var Depths = []int{10, 30, RecallDepth}

// Method is a retrieval compared by RunRelevance.
type Method struct {
	Name     string
	Retrieve RetrieveFunc
}

// Relevance holds each method's top RecallDepth per query, as retrieved, and the judge's rating of each
// result: Lists[method][query][rank], Ratings likewise, -1 when the rating failed.
type Relevance struct {
	Queries []Query
	Methods []string
	Lists   [][][]search.Result
	Ratings [][][]int
	Latency [][]time.Duration // retrieval time, [method][query]
	// Note, when set, is written above the results.
	Note string
}

// DepthSummary aggregates a method's ratings over the top Depth results of every query.
type DepthSummary struct {
	Rated   int
	Unrated int
	Mean    float64 // mean rating of a result
	Good    float64 // share of results rated 2 or more
	Found   int     // queries whose known target is in the top Depth
}

// RunRelevance retrieves the top RecallDepth of every query with each method, then has judge rate every
// result. A work in several methods' lists is rated once.
func RunRelevance(ctx context.Context, judge *Judge, methods []Method, queries []Query) (Relevance, error) {
	n := len(methods)
	rel := Relevance{Queries: queries, Lists: make([][][]search.Result, n), Ratings: make([][][]int, n),
		Latency: make([][]time.Duration, n)}
	for _, m := range methods {
		rel.Methods = append(rel.Methods, m.Name)
	}

	// Untimed first call: the first query embedding loads the model, the first SQL warms the caches.
	if len(queries) > 0 {
		for _, m := range methods {
			if _, err := m.Retrieve(ctx, queries[0].Query, RecallDepth); err != nil {
				return rel, fmt.Errorf("%s: %w", m.Name, err)
			}
		}
	}

	for qi, q := range queries {
		var union []search.Result
		seen := map[string]bool{}
		for mi, m := range methods {
			start := time.Now()
			list, err := m.Retrieve(ctx, q.Query, RecallDepth)
			if err != nil {
				return rel, fmt.Errorf("%s, %q: %w", m.Name, q.Query, err)
			}

			rel.Latency[mi] = append(rel.Latency[mi], time.Since(start))
			rel.Lists[mi] = append(rel.Lists[mi], list)
			for _, b := range list {
				if !seen[b.ID] {
					seen[b.ID] = true
					union = append(union, b)
				}
			}
		}

		if err := judge.RateAll(ctx, q.Query, union); err != nil {
			return rel, err
		}

		for mi := range methods {
			ratings := make([]int, len(rel.Lists[mi][qi]))
			for i, b := range rel.Lists[mi][qi] {
				ratings[i] = -1
				if r, ok := judge.Rating(q.Query, b); ok {
					ratings[i] = r
				}
			}

			rel.Ratings[mi] = append(rel.Ratings[mi], ratings)
		}

		cached, asked, failed := judge.Stats()
		judge.logger().Info("rated", "query", qi+1, "of", len(queries), "union", len(union), "cached", cached,
			"asked", asked, "failed", failed)
	}

	return rel, nil
}

// Summarize aggregates method m's top depth results over every query.
func (r Relevance) Summarize(m, depth int) DepthSummary {
	var s DepthSummary
	var sum, good int
	for qi, ratings := range r.Ratings[m] {
		for _, x := range ratings[:min(depth, len(ratings))] {
			if x < 0 {
				s.Unrated++
				continue
			}

			s.Rated++
			sum += x
			if x >= 2 {
				good++
			}
		}

		if k := rank(r.Lists[m][qi], r.Queries[qi].Targets); k > 0 && k <= depth {
			s.Found++
		}
	}

	if s.Rated > 0 {
		s.Mean, s.Good = float64(sum)/float64(s.Rated), float64(good)/float64(s.Rated)
	}

	return s
}

// queryMean is method m's mean rating over the top depth results of query qi, -1 when none is rated.
func (r Relevance) queryMean(m, qi, depth int) float64 {
	sum, n := 0, 0
	ratings := r.Ratings[m][qi]
	for _, x := range ratings[:min(depth, len(ratings))] {
		if x >= 0 {
			sum += x
			n++
		}
	}

	if n == 0 {
		return -1
	}

	return float64(sum) / float64(n)
}

// overlap is how many works methods a and b share in their top depth for query qi.
func (r Relevance) overlap(a, b, qi, depth int) int {
	in := map[string]bool{}
	la, lb := r.Lists[a][qi], r.Lists[b][qi]
	for _, x := range la[:min(depth, len(la))] {
		in[x.ID] = true
	}

	n := 0
	for _, x := range lb[:min(depth, len(lb))] {
		if in[x.ID] {
			n++
		}
	}

	return n
}

// WriteMarkdown writes the summary per method, the overlap between methods, and an appendix per query.
func (r Relevance) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	if r.Note != "" {
		b.WriteString("> " + strings.ReplaceAll(r.Note, "\n", " ") + "\n\n")
	}

	r.writeSummary(&b)
	r.writeOverlap(&b)
	r.writePerQuery(&b)

	_, err := io.WriteString(w, b.String())

	return err
}

func (r Relevance) writeSummary(b *strings.Builder) {
	targets := 0
	for _, q := range r.Queries {
		if len(q.Targets) > 0 {
			targets++
		}
	}

	fmt.Fprintf(b, "## Summary\n\nEach method's top %d for each of the %d queries, without re-ranking. An LLM rated every "+
		"result from 0 (not relevant) to %d (strong match), reading the query and the book's title, authors, year and "+
		"description. Mean: mean rating of a result. ≥2: share of results rated 2 or 3. Target: queries (of %d) whose "+
		"known book is in the list. Retrieval: time to get the top %d after a warm-up call, mean and slowest; vector includes embedding the query.\n\n",
		RecallDepth, len(r.Queries), MaxRating, targets, RecallDepth)

	b.WriteString("| Method |")
	for _, title := range []string{"Mean", "≥2", "Target"} {
		for _, d := range Depths {
			fmt.Fprintf(b, " %s @%d |", title, d)
		}
	}

	b.WriteString(" Retrieval | Unrated |\n|---|" + strings.Repeat("---|", 3*len(Depths)+2) + "\n")
	for m, name := range r.Methods {
		sums := make([]DepthSummary, len(Depths))
		for i, d := range Depths {
			sums[i] = r.Summarize(m, d)
		}

		fmt.Fprintf(b, "| %s |", cell(name))
		for _, s := range sums {
			fmt.Fprintf(b, " %.2f |", s.Mean)
		}

		for _, s := range sums {
			fmt.Fprintf(b, " %.0f%% |", 100*s.Good)
		}

		for _, s := range sums {
			fmt.Fprintf(b, " %d |", s.Found)
		}

		mean, slowest := latency(r.Latency[m])
		fmt.Fprintf(b, " %s / %s | %d |\n", mean, slowest, sums[len(sums)-1].Unrated)
	}
}

func (r Relevance) writeOverlap(b *strings.Builder) {
	for a := range r.Methods {
		for c := a + 1; c < len(r.Methods); c++ {
			fmt.Fprintf(b, "\nWorks shared by %s and %s, mean per query:", r.Methods[a], r.Methods[c])
			for i, d := range Depths {
				total := 0
				for qi := range r.Queries {
					total += r.overlap(a, c, qi, d)
				}

				sep := ","
				if i == len(Depths)-1 {
					sep = "."
				}

				fmt.Fprintf(b, " %.1f of the top %d%s", float64(total)/float64(len(r.Queries)), d, sep)
			}

			b.WriteString("\n")
		}
	}
}

func (r Relevance) writePerQuery(b *strings.Builder) {
	b.WriteString("\n## Per query\n\nMean rating of the top 10 / 30 / 100; shared: works in both top 100s.\n\n| # | Kind | Query |")
	for _, name := range r.Methods {
		fmt.Fprintf(b, " %s |", cell(name))
	}

	cols := 3 + len(r.Methods)
	if len(r.Methods) == 2 {
		b.WriteString(" Shared |")
		cols++
	}

	b.WriteString("\n|" + strings.Repeat("---|", cols) + "\n")
	for qi, q := range r.Queries {
		fmt.Fprintf(b, "| %d | %s | %s |", qi+1, cell(q.Kind), cell(cutText(q.Query, 70)))
		for m := range r.Methods {
			var parts []string
			for _, d := range Depths {
				if x := r.queryMean(m, qi, d); x >= 0 {
					parts = append(parts, fmt.Sprintf("%.2f", x))
				} else {
					parts = append(parts, "–")
				}
			}

			fmt.Fprintf(b, " %s |", strings.Join(parts, " / "))
		}

		if len(r.Methods) == 2 {
			fmt.Fprintf(b, " %d |", r.overlap(0, 1, qi, RecallDepth))
		}

		b.WriteString("\n")
	}
}

// latency returns the mean and the slowest of ds, rounded to the millisecond.
func latency(ds []time.Duration) (mean, slowest time.Duration) {
	if len(ds) == 0 {
		return 0, 0
	}

	var sum time.Duration
	for _, d := range ds {
		sum += d
		slowest = max(slowest, d)
	}

	return (sum / time.Duration(len(ds))).Round(time.Millisecond), slowest.Round(time.Millisecond)
}
