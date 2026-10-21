package bench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nem0z/book-finder/internal/search"
)

// reverser scores the last text highest, so re-ranking reverses the candidates.
type reverser struct{}

func (reverser) Scores(_ context.Context, _ string, texts []string) ([]float64, error) {
	out := make([]float64, len(texts))
	for i := range out {
		out[i] = float64(i)
	}

	return out, nil
}

func results(ids ...string) []search.Result {
	out := make([]search.Result, len(ids))
	for i, id := range ids {
		out[i] = search.Result{ID: id, Title: "Book " + id, Authors: []string{"Author"}, Year: 2000 + i}
	}

	return out
}

func retrieve(lists map[string][]search.Result) RetrieveFunc {
	return func(_ context.Context, query string, n int) ([]search.Result, error) {
		if query == "fail" {
			return nil, errors.New("down")
		}

		l := lists[query]
		return l[:min(n, len(l))], nil
	}
}

func TestRun(t *testing.T) {
	queries := []Query{
		{Query: "q1", Kind: "plot", Title: "Target", Targets: []string{"T", "T2"}},
		{Query: "q2", Kind: "mood", Title: "Lost", Targets: []string{"X"}},
		{Query: "judge me", Kind: "era"},
	}
	lists := map[string][]search.Result{
		"q1":       results("A", "B", "T2", "C"),
		"q2":       results("A", "B"),
		"judge me": results("J1", "J|2"),
	}
	pipelines := []Pipeline{{Name: "short", Retrieve: retrieve(lists), Candidates: 3}, {Name: "default", Retrieve: retrieve(lists)}}

	rep, err := Run(context.Background(), reverser{}, pipelines, queries, 2)
	if err != nil {
		t.Fatal(err)
	}

	rep.Note = "On a sample."

	if pipelines[1].Candidates != 0 || rep.Pipelines[1].Candidates != search.DefaultCandidates {
		t.Errorf("default candidates: %+v, %+v", pipelines[1], rep.Pipelines[1])
	}

	// short re-ranks A, B, T2 into T2, B; default re-ranks all four into C, T2.
	short, def := rep.Outcomes[0][0], rep.Outcomes[1][0]
	if short.RetrievalRank != 3 || short.FinalRank != 1 || def.RetrievalRank != 3 || def.FinalRank != 2 {
		t.Errorf("ranks: short %+v, default %+v", short, def)
	}

	if s := rep.Summarize(0); s.Queries != 2 || s.Found != 1 || s.Hit1 != 1 || s.HitAll != 1 || s.MRR != 0.5 {
		t.Errorf("summary short: %+v", s)
	}

	if s := rep.Summarize(1); s.Hit1 != 0 || s.HitAll != 1 || s.MRR != 0.25 {
		t.Errorf("summary default: %+v", s)
	}

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatal(err)
	}

	md := b.String()
	for _, want := range []string{
		"> On a sample.\n\n## Summary",
		"| short | 3 | 1/2 | 1 | 1 | 0.50 |",
		"| # | Kind | Target | short | default |\n|---|---|---|---|---|\n| 1 | plot | Target | 3 → 1 | 3 → 2 |\n| 2 | mood | Lost | – → – | – → – |\n",
		"## Query 3, judged by hand\n\n> judge me\n\n| # | short | default |\n|---|---|---|\n| 1 | Book J\\|2, Author (2001) | Book J\\|2, Author (2001) |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown misses %q:\n%s", want, md)
		}
	}

	if _, err := Run(context.Background(), nil, pipelines, []Query{{Query: "fail"}}, 2); err == nil {
		t.Error("expected the retrieval error")
	}
}

func TestRunWithoutReranker(t *testing.T) {
	lists := map[string][]search.Result{"q": results("A", "T", "B")}
	rep, err := Run(context.Background(), nil, []Pipeline{{Name: "p", Retrieve: retrieve(lists)}}, []Query{{Query: "q", Targets: []string{"T"}}}, 2)
	if err != nil {
		t.Fatal(err)
	}

	if o := rep.Outcomes[0][0]; o.FinalRank != 2 || len(o.Results) != 2 {
		t.Errorf("outcome: %+v", o)
	}
}

func TestLoadQueries(t *testing.T) {
	qs, err := LoadQueries("")
	if err != nil {
		t.Fatal(err)
	}

	judged := 0
	for _, q := range qs {
		if strings.TrimSpace(q.Query) == "" || q.Kind == "" || (len(q.Targets) > 0) != (q.Title != "") {
			t.Errorf("incomplete query: %+v", q)
		}

		if len(q.Targets) == 0 {
			judged++
		}
	}

	if len(qs) < 40 || judged != 1 {
		t.Errorf("%d queries, %d judged by hand", len(qs), judged)
	}

	path := filepath.Join(t.TempDir(), "q.json")
	if err := os.WriteFile(path, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadQueries(path); !errors.Is(err, ErrNoQueries) {
		t.Errorf("empty file: %v", err)
	}
}
