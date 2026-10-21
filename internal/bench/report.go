package bench

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// WriteMarkdown writes the report as Markdown tables: a summary per pipeline, the target's rank per
// query, and the results of the queries without targets side by side.
func (r Report) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	r.writeSummary(&b)
	r.writeRanks(&b)
	r.writeJudged(&b)

	_, err := io.WriteString(w, b.String())

	return err
}

func (r Report) writeSummary(b *strings.Builder) {
	b.WriteString("## Summary\n\n")
	fmt.Fprintf(b, "Over the queries with a known target. Found: target in the first %d candidates. Hit: target in "+
		"the top %d after re-ranking. Times are means per query.\n\n", RecallDepth, r.Limit)
	fmt.Fprintf(b, "| Pipeline | Re-ranked | Found | Hit@1 | Hit@%d | MRR | Retrieval | Re-rank |\n", r.Limit)
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for i, p := range r.Pipelines {
		s := r.Summarize(i)
		fmt.Fprintf(b, "| %s | %d | %d/%d | %d | %d | %.2f | %s | %s |\n", cell(p.Name), p.Candidates, s.Found, s.Queries,
			s.Hit1, s.HitAll, s.MRR, s.Retrieval.Round(time.Millisecond), s.Rerank.Round(time.Millisecond))
	}
}

func (r Report) writeRanks(b *strings.Builder) {
	b.WriteString("\n## Target rank\n\nAmong the candidates → after re-ranking; – when missing.\n\n| # | Kind | Target |")
	r.writePipelineHeader(b, 3)
	for j, q := range r.Queries {
		if len(q.Targets) == 0 {
			continue
		}

		fmt.Fprintf(b, "| %d | %s | %s |", j+1, cell(q.Kind), cell(q.Title))
		for i := range r.Pipelines {
			o := r.Outcomes[i][j]
			fmt.Fprintf(b, " %s → %s |", rankCell(o.RetrievalRank), rankCell(o.FinalRank))
		}

		b.WriteString("\n")
	}
}

func (r Report) writeJudged(b *strings.Builder) {
	for j, q := range r.Queries {
		if len(q.Targets) > 0 {
			continue
		}

		fmt.Fprintf(b, "\n## Query %d, judged by hand\n\n> %s\n\n| # |", j+1, q.Query)
		r.writePipelineHeader(b, 1)
		for k := range r.Limit {
			fmt.Fprintf(b, "| %d |", k+1)
			for i := range r.Pipelines {
				fmt.Fprintf(b, " %s |", resultCell(r.Outcomes[i][j], k))
			}

			b.WriteString("\n")
		}
	}
}

// writePipelineHeader ends a table header row that has lead columns with a column per pipeline, then
// writes the separator row.
func (r Report) writePipelineHeader(b *strings.Builder, lead int) {
	for _, p := range r.Pipelines {
		fmt.Fprintf(b, " %s |", cell(p.Name))
	}

	b.WriteString("\n|" + strings.Repeat("---|", lead+len(r.Pipelines)) + "\n")
}

func resultCell(o Outcome, k int) string {
	if k >= len(o.Results) {
		return ""
	}

	res := o.Results[k]
	s := res.Title
	if len(res.Authors) > 0 {
		s += ", " + res.Authors[0]
	}

	if res.Year > 0 {
		s += " (" + strconv.Itoa(res.Year) + ")"
	}

	return cell(s)
}

func rankCell(rank int) string {
	if rank == 0 {
		return "–"
	}

	return strconv.Itoa(rank)
}

// cell makes s safe inside a Markdown table cell.
func cell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}
