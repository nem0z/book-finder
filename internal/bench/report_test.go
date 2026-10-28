package bench

import (
	"strings"
	"testing"
	"time"
)

func TestWriteMarkdown(t *testing.T) {
	ev, _ := testEval(t)
	ev.Note = "a note"
	ev.Rerank = []time.Duration{time.Second, 3 * time.Second}

	var b strings.Builder
	if err := ev.WriteMarkdown(&b); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"# Accuracy of the search\n\n> a note",
		"## Retrieval: the top 2",
		"- 0.5 relevant works per query on average (25% of the pool), mean rating 1.00.",
		"## Final top 10",
		"| 1.50 | 50% | 25% | 25% |",
		"Use of the pool: the top 10 holds 0.5 of the 0.5 relevant works",
		"Re-ranking: the top 10 goes from mean 1.50",
		"| plot | 1 | 1.0 · 0 | 1.83 · 67% · 0.87 |",
		"| S | 1 |",
		"| plot | 0.75 · 50% (4) | 1.25 · 25% (8) |",
		"## The 2 worst queries",
		"Causes: retrieval 1, catalogue gap? 1.",
		"1. Book A, Author (1900): **3** (mood 0), r",
		"## Flagged queries",
		"| 2 * | vibe | XL | q2 | 0 | 0 | 0.5 | 0.63 |",
		"Re-ranking the pool: 2s / 3s",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("markdown misses %q:\n%s", want, b.String())
		}
	}

	// Without re-ranking, the report has no pool use or re-rank time.
	ev.Final = nil
	b.Reset()
	if err := ev.WriteMarkdown(&b); err != nil {
		t.Fatal(err)
	}

	if s := b.String(); !strings.Contains(s, "## Top 10, without re-ranking") || strings.Contains(s, "Use of the pool") || strings.Contains(s, "Re-ranking the pool") {
		t.Errorf("without re-ranking:\n%s", s)
	}
}
