package bench

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nem0z/book-finder/internal/search"
)

// reverser scores later texts higher, so re-ranking reverses the order.
type reverser struct{}

func (reverser) Scores(_ context.Context, _ string, texts []string) ([]float64, error) {
	out := make([]float64, len(texts))
	for i := range texts {
		out[i] = float64(i)
	}

	return out, nil
}

func ids(list []search.Result) []string {
	var out []string
	for _, w := range list {
		out = append(out, w.ID)
	}

	return out
}

// testEval retrieves, rates and re-ranks two queries: q1 retrieves C, A, B, D, E, F and q2 D, C, with a pool
// of 2. Ratings: A 3, B 2, C 0, D 1, E 3, F 2.
func testEval(t *testing.T) (*Eval, *fakeJudge) {
	t.Helper()
	ctx := context.Background()
	llm := &fakeJudge{ratings: map[string]int{"A": 3, "B": 2, "C": 0, "D": 1, "E": 3, "F": 2}}
	j, err := OpenJudge(llm, "m", filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })

	lists := map[string][]search.Result{"q1": works("C", "A", "B", "D", "E", "F"), "q2": works("D", "C")}
	retrieve := func(_ context.Context, query string, n int) ([]search.Result, error) {
		if query == "" {
			return nil, errors.New("empty")
		}

		return lists[query][:min(n, len(lists[query]))], nil
	}

	queries := []Query{
		{Query: "q1", Category: "plot", Length: "S"},
		{Query: "q2", Category: "vibe", Length: "XL", Flag: "translation"},
	}
	ev, err := Retrieve(ctx, retrieve, queries, 2, DefaultDepth)
	if err != nil {
		t.Fatal(err)
	}

	if err := ev.Rate(ctx, j); err != nil {
		t.Fatal(err)
	}

	if err := ev.RerankPool(ctx, reverser{}); err != nil {
		t.Fatal(err)
	}

	return ev, llm
}

func TestEval(t *testing.T) {
	ev, llm := testEval(t)

	// Every retrieved work is rated once per query.
	if n := llm.calls.Load(); n != 8 {
		t.Errorf("%d ratings asked", n)
	}

	if got := ids(ev.final(0)); !slices.Equal(got, []string{"A", "C", "B", "D", "E", "F"}) || len(ev.Rerank) != 2 || len(ev.Retrieval) != 2 {
		t.Errorf("final order %v, times %v %v", got, ev.Rerank, ev.Retrieval)
	}

	if got := ids(ev.retrieved(0)); !slices.Equal(got, []string{"C", "A", "B", "D", "E", "F"}) {
		t.Errorf("retrieval order %v", got)
	}

	// The final top 10s: A 3, C 0, B 2, D 1, E 3, F 2, then C 0, D 1.
	if mean, good, three, zero, n := ev.ratings(ev.final, 0, 10, all); n != 8 || mean != 1.5 || good != 0.5 || three != 0.25 || zero != 0.25 {
		t.Errorf("ratings: %v, %v, %v, %v, %d", mean, good, three, zero, n)
	}

	if mean, _, _, _, n := ev.ratings(ev.retrieved, 0, 2, func(qi int) bool { return qi == 0 }); n != 2 || mean != 1.5 {
		t.Errorf("q1's pool: %v, %d", mean, n)
	}

	if x, y := ev.ndcg10(ev.final, 0), ev.ndcg10(ev.retrieved, 0); x <= y || x >= 1 {
		t.Errorf("nDCG@10 of q1: final %v, retrieved %v", x, y)
	}

	// Plot is min(rating, 2) and asked of every query; era is never asked.
	if mean, zero, n := ev.dimension(ev.final, "plot", 10); n != 8 || mean != 1.25 || zero != 0.25 {
		t.Errorf("plot: %v, %v, %d", mean, zero, n)
	}

	if _, _, n := ev.dimension(ev.final, "era", 10); n != 0 {
		t.Errorf("era should not be asked: %d", n)
	}

	// q1's pool (C, A) has A; B, E and F are below it.
	if s := ev.stats(0); s.poolGood != 1 || s.belowGood != 3 || s.top10Good != 4 || s.possible() != 1 || !strings.HasPrefix(s.cause(), "retrieval") {
		t.Errorf("stats: %+v, %s", s, s.cause())
	}

	if s := (queryStats{poolGood: 12, top10Good: 5}); !strings.HasPrefix(s.cause(), "ranking") {
		t.Errorf("ranking cause: %s", s.cause())
	}

	if s := (queryStats{poolGood: 1, belowGood: 1}); !strings.HasPrefix(s.cause(), "catalogue gap?") {
		t.Errorf("gap cause: %s", s.cause())
	}

	// Without re-ranking, the final order is the retrieval order.
	ev.Final = nil
	if got := ids(ev.final(0)); !slices.Equal(got, []string{"C", "A", "B", "D", "E", "F"}) {
		t.Errorf("no re-ranking: %v", got)
	}

	if _, err := Retrieve(context.Background(), func(context.Context, string, int) ([]search.Result, error) {
		return nil, errors.New("down")
	}, []Query{{Query: "q"}}, 2, 10); err == nil {
		t.Error("a retrieval error should stop the run")
	}
}
