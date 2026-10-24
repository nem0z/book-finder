package bench

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nem0z/book-finder/internal/search"
)

func TestRerankEffect(t *testing.T) {
	ctx := context.Background()
	// 40 works, W0 to W39: the re-ranker reverses whatever it gets.
	ids := make([]string, 40)
	ratings := map[string]int{}
	for i := range ids {
		ids[i] = string(rune('A'+i/26)) + string(rune('a'+i%26))
		ratings[ids[i]] = 0
	}

	ratings[ids[39]] = 3 // rank 40, the last: the re-ranker of all 40 puts it first
	ratings[ids[0]] = 2  // rank 1: pushed out of the top 10 by any re-ranking
	lists := map[string][]search.Result{"q": works(ids...)}
	j, err := OpenJudge(&fakeJudge{ratings: map[string]int{}}, "m", filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	for _, w := range works(ids...) {
		j.cache[j.key("q", w)] = Judgment{Key: j.key("q", w), Rating: ratings[w.ID]}
	}

	queries := []Query{{Query: "q", Kind: "plot", Title: "Target", Targets: []string{ids[39]}}}
	e, err := RunRerankEffect(ctx, j, reverser{}, retrieve(lists), queries, []int{30, 100})
	if err != nil {
		t.Fatal(err)
	}

	// Top 30: ranks 1-30 reversed, 31-40 in place. All 100 (here 40): everything reversed.
	if e.After[0][0][0].ID != ids[29] || e.After[0][0][30].ID != ids[30] || e.After[1][0][0].ID != ids[39] {
		t.Errorf("orders: %s %s %s", e.After[0][0][0].ID, e.After[0][0][30].ID, e.After[1][0][0].ID)
	}

	// The best top 10 is the work rated 3, then the one rated 2.
	if before, after := e.ndcg10(0, e.Before[0]), e.ndcg10(0, e.After[1][0]); before <= 0 || after <= before || after >= 1 {
		t.Errorf("nDCG@10: %v before, %v after", before, after)
	}

	all := transitions(e.moves(1), func(int) bool { return true })
	good := transitions(e.moves(1), func(r int) bool { return r >= 2 })
	if good[2][0] != 1 || good[0][2] != 1 || all[0][2] != 10 {
		t.Errorf("transitions: all %v, good %v", all, good)
	}

	var b strings.Builder
	if err := e.WriteMarkdown(&b); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"| before re-ranking | 0.20 / 10% | 0.00 / 0% | 0.30 / 10% | 0.337 | – |",
		"| 11–30 | 10 / 0 / 10 | 10 / 0 / 10 | 0 / 0 / 0 |",
		"From 31–100: 1 rated 2–3, 9 rated 0. Pushed out of the top 10: 1 rated 2–3, 9 rated 0.",
		"- 40 → 1, rated 3: Book Bn, Author (1900), for query 1 (q)",
		"| 1 | Target | 40 | 40 | 1 |",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("markdown misses %q:\n%s", want, b.String())
		}
	}

	var svg strings.Builder
	if err := e.WriteSVG(&svg, 1); err != nil {
		t.Fatal(err)
	}

	if n := strings.Count(svg.String(), `r="2.6"`); n != 40 || !strings.HasSuffix(svg.String(), "</svg>\n") {
		t.Errorf("svg: %d dots", n)
	}

	// Re-ranking all 40 puts the work rated 3 first: the curve starts at (3+0+0)/3 = 1.
	if c := e.curve(e.After[1]); len(c) != RecallDepth || c[0] != 1 || c[39] == 0 || c[60] != 0 {
		t.Errorf("curve: %v", c[:5])
	}

	svg.Reset()
	if err := e.WriteCurveSVG(&svg); err != nil || strings.Count(svg.String(), "<polyline") != 3 {
		t.Errorf("curve svg: %v", err)
	}
}
