package search

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

type fakeReranker struct {
	scores []float64
	err    error
	query  string
	cards  []string
}

func (f *fakeReranker) Scores(_ context.Context, query string, texts []string) ([]float64, error) {
	f.query, f.cards = query, texts
	return f.scores, f.err
}

func TestRerank(t *testing.T) {
	results := []Result{
		{ID: "OL1W", Title: "A", Authors: []string{"X", "Y"}, Description: strings.Repeat("word ", 200)},
		{ID: "OL2W", Title: "B", Description: "short"},
		{ID: "OL3W", Title: "C"},
	}
	rr := &fakeReranker{scores: []float64{0.2, 0.9, 0.5}}

	got, err := Rerank(context.Background(), rr, "q", results, 2)
	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}

	if !slices.Equal(ids, []string{"OL2W", "OL3W"}) || got[0].RerankScore != 0.9 {
		t.Errorf("order: %v, %+v", ids, got)
	}

	if results[0].RerankScore != 0 {
		t.Error("the input should not be modified")
	}

	if rr.query != "q" || !strings.HasPrefix(rr.cards[0], "A by X, Y\n\nword") || len(rr.cards[0]) > len("A by X, Y\n\n")+cardDescriptionBytes {
		t.Errorf("card: %q", rr.cards[0])
	}

	if rr.cards[2] != "C\n\n" {
		t.Errorf("card without authors or description: %q", rr.cards[2])
	}
}

func TestRerankErrors(t *testing.T) {
	ctx := context.Background()
	results := []Result{{ID: "OL1W"}, {ID: "OL2W"}}

	if _, err := Rerank(ctx, &fakeReranker{err: errors.New("down")}, "q", results, 10); err == nil {
		t.Error("expected the re-ranker's error")
	}

	if _, err := Rerank(ctx, &fakeReranker{scores: []float64{1}}, "q", results, 10); err == nil {
		t.Error("expected an error for a missing score")
	}

	if got, err := Rerank(ctx, &fakeReranker{}, "q", nil, 10); err != nil || len(got) != 0 {
		t.Errorf("no results: %v, %v", got, err)
	}
}

func TestCut(t *testing.T) {
	if got := cut("one two three", 9); got != "one two" {
		t.Errorf("got %q", got)
	}

	if got := cut("short", 9); got != "short" {
		t.Errorf("got %q", got)
	}
}
