package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nem0z/book-finder/internal/search"
)

// fakeRecJudge rates books by title ("Book A" → ratings["A"]), scores plot as min(rating, 2), mood 0 and the
// other dimensions as not asked; a missing rating fails.
type fakeRecJudge struct {
	ratings map[string]int
	calls   atomic.Int64
}

func (f *fakeRecJudge) JSON(_ context.Context, system, user, schemaName string, schema json.RawMessage, out any) error {
	f.calls.Add(1)
	if system != recommendationPrompt || schemaName != "recommendation" || !json.Valid(schema) {
		return errors.New("unexpected prompt")
	}

	book, _, _ := strings.Cut(strings.SplitN(user, "\nBook: Book ", 2)[1], "\n")
	r, ok := f.ratings[book]
	if !ok {
		return errors.New("no answer")
	}

	dims := []string{fmt.Sprintf(`"plot": %d`, min(r, 2)), `"mood": 0`}
	for _, d := range RecommendationDimensions[2:] {
		dims = append(dims, fmt.Sprintf(`"%s": -1`, d))
	}

	return json.Unmarshal(fmt.Appendf(nil, `{"rating": %d, "dimensions": {%s}, "reason": "r"}`, r, strings.Join(dims, ", ")), out)
}

func TestRecommendation(t *testing.T) {
	ctx := context.Background()
	llm := &fakeRecJudge{ratings: map[string]int{"A": 3, "B": 2, "C": 0, "D": 1, "E": 3}}
	j, err := OpenJudgeRubric(llm, "m", filepath.Join(t.TempDir(), "j.jsonl"), RecommendationRubric)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	queries := []Query{
		{Query: "q1", Category: "plot", Length: "S"},
		{Query: "q2", Category: "vibe", Length: "XL", Flag: "translation"},
	}
	first := map[string][]search.Result{"q1": works("C", "A", "B"), "q2": works("D", "C")}
	second := map[string][]search.Result{"q1": works("A", "E", "C"), "q2": works("D")}
	methods := []Method{{Name: "first", Retrieve: retrieve(first)}, {Name: "second", Retrieve: retrieve(second)}}

	r, err := RetrieveRecommendations(ctx, methods, queries)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Rate(ctx, j); err != nil {
		t.Fatal(err)
	}

	// q1 rates A, B, C, E once each; q2 rates C, D.
	if n := llm.calls.Load(); n != 6 {
		t.Errorf("%d ratings asked", n)
	}

	orders, took, err := r.RerankMethod(ctx, reverser{}, 0, 2)
	if err != nil || len(took) != 2 || orders[0][0].ID != "A" || orders[0][1].ID != "C" || orders[0][2].ID != "B" {
		t.Fatalf("re-rank: %v, %v", orders, err)
	}

	r.AddMethod("first re-ranked", 0, orders, took)
	r.Baseline, r.Proposed, r.Evaluated, r.EvaluatedBase, r.Pool = 0, 2, 2, 0, 2

	// q1: the pool (C, A) has A; B and E, rated 2 and 3, are elsewhere; the top 10 (A, C, B) has A and B.
	if s := r.stats(0); s.poolGood != 1 || s.elsewhereGood != 2 || s.top10Good != 2 || s.possible() != 1 || !strings.HasPrefix(s.cause(), "catalogue gap?") {
		t.Errorf("stats: %+v, %s", s, s.cause())
	}

	if s := (queryStats{poolGood: 12, top10Good: 5}); !strings.HasPrefix(s.cause(), "ranking") {
		t.Errorf("ranking cause: %s", s.cause())
	}

	if s := (queryStats{poolGood: 2, elsewhereGood: 5, top10Good: 2}); !strings.HasPrefix(s.cause(), "retrieval") {
		t.Errorf("retrieval cause: %s", s.cause())
	}

	// q1's best 10 are A, E (3), B (2), C (0). "second" gets A, E, C: better than "first", C, A, B.
	if x, y := r.ndcg10(1, 0), r.ndcg10(0, 0); x <= y || x >= 1 {
		t.Errorf("nDCG@10: second %v, first %v", x, y)
	}

	if mean, good, n := r.relevance(1, 0, 10, all); n != 4 || mean != 7.0/4 || good != 0.5 {
		t.Errorf("relevance of second: %v, %v, %d", mean, good, n)
	}

	if mean, zero, n := r.dimension(0, "plot"); n != 5 || zero != 0.4 || mean != 5.0/5 {
		t.Errorf("plot of first: %v, %v, %d", mean, zero, n)
	}

	if _, _, n := r.dimension(0, "era"); n != 0 {
		t.Errorf("era should not be asked: %d", n)
	}

	var b strings.Builder
	if err := r.WriteMarkdown(&b); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"# Accuracy of first re-ranked",
		"## Retrieval: the top 2 (first)",
		"## The 2 worst queries",
		"1. Book A, Author (1900): **3** (mood 0), r",
		"# Appendix: every method",
		"| Method | Mean | ≥2 | nDCG@10 | nDCG@10, unflagged | Retrieval | Re-rank |",
		"Per query, nDCG@10 against first (better / worse / same): second 1 / 0 / 1; first re-ranked",
		"| Category | Queries | first | second | first re-ranked |",
		"| plot | 1 |",
		"| S | 1 |",
		"| plot | 1.00 · 40% (5) |",
		"## Flagged queries",
		"| 2 * | vibe | XL | q2 |",
		"## Examples",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("markdown misses %q:\n%s", want, b.String())
		}
	}
}

func TestLoadRecommendationQueries(t *testing.T) {
	qs, err := LoadRecommendationQueries("")
	if err != nil {
		t.Fatal(err)
	}

	cats, lengths, flagged := map[string]int{}, map[string]int{}, 0
	for _, q := range qs {
		cats[q.Category]++
		lengths[q.Length]++
		if q.Flag != "" {
			flagged++
		}

		if len(q.Targets) > 0 || strings.TrimSpace(q.Query) == "" {
			t.Errorf("query %+v", q)
		}
	}

	if len(qs) != 70 || len(cats) != 7 || lengths["S"] != 14 || lengths["XL"] != 14 || flagged != 2 {
		t.Errorf("%d queries, categories %v, lengths %v, %d flagged", len(qs), cats, lengths, flagged)
	}

	for c, n := range cats {
		if n != 10 {
			t.Errorf("%s: %d queries", c, n)
		}
	}
}

func TestRubricCheck(t *testing.T) {
	dims := map[string]int{}
	for _, d := range RecommendationDimensions {
		dims[d] = NotApplicable
	}

	if err := RecommendationRubric.check(2, dims); err != nil {
		t.Errorf("valid: %v", err)
	}

	dims["plot"] = 3
	if RecommendationRubric.check(2, dims) == nil {
		t.Error("dimension out of range")
	}

	delete(dims, "plot")
	if RecommendationRubric.check(2, dims) == nil {
		t.Error("missing dimension")
	}

	if KnownItemRubric.check(4, nil) == nil || KnownItemRubric.check(3, nil) != nil {
		t.Error("known-item rating range")
	}
}
