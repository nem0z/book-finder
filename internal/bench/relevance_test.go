package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nem0z/book-finder/internal/enrich"
	"github.com/nem0z/book-finder/internal/llm"
	"github.com/nem0z/book-finder/internal/search"
)

// fakeJudge rates books by their title ("Book A" → ratings["A"]); a missing rating fails.
type fakeJudge struct {
	ratings map[string]int
	calls   atomic.Int64
}

func (f *fakeJudge) JSON(_ context.Context, system, user, schemaName string, _ json.RawMessage, out any) error {
	f.calls.Add(1)
	if system != JudgePrompt || schemaName != judgeSchemaName {
		return errors.New("unexpected prompt")
	}

	book, _, _ := strings.Cut(strings.SplitN(user, "\nBook: Book ", 2)[1], "\n")
	r, ok := f.ratings[book]
	if !ok {
		return errors.New("no answer")
	}

	return json.Unmarshal(fmt.Appendf(nil, `{"rating": %d, "reason": "because"}`, r), out)
}

// works returns results that read the same in every list, as a work does.
func works(ids ...string) []search.Result {
	out := results(ids...)
	for i := range out {
		out[i].Year = 1900
	}

	return out
}

func TestRelevance(t *testing.T) {
	ctx := context.Background()
	cache := filepath.Join(t.TempDir(), "judgments.jsonl")
	llm := &fakeJudge{ratings: map[string]int{"A": 3, "B": 2, "C": 0, "E": 7}}
	lists := map[string][]search.Result{"q1": works("A", "B", "C", "E")}
	other := map[string][]search.Result{"q1": works("B", "C", "D")}
	prepared := 0
	prepare := func(context.Context, string) (time.Duration, error) { prepared++; return time.Hour, nil }
	methods := []Method{{Name: "a", Retrieve: retrieve(lists)}, {Name: "b", Retrieve: retrieve(other), Prepare: prepare}}
	queries := []Query{{Query: "q1", Kind: "plot", Title: "C", Targets: []string{"C"}}}

	j, err := OpenJudge(llm, "m", cache)
	if err != nil {
		t.Fatal(err)
	}

	rel, err := RunRelevance(ctx, j, methods, queries)
	if err != nil {
		t.Fatal(err)
	}

	// D fails and E's rating is out of range: both are unrated.
	if cached, asked, failed := j.Stats(); cached != 3 || asked != 5 || failed != 2 || llm.calls.Load() != 5 {
		t.Errorf("stats: %d cached, %d asked, %d failed, %d calls", cached, asked, failed, llm.calls.Load())
	}

	if prepared != 2 || rel.Latency[1][0] < time.Hour || rel.Latency[0][0] >= time.Hour {
		t.Errorf("prepare: %d calls, latencies %v", prepared, rel.Latency)
	}

	if s := rel.Summarize(0, 10); s.Rated != 3 || s.Unrated != 1 || s.Mean != 5.0/3 || s.Good != 2.0/3 || s.Found != 1 {
		t.Errorf("summary a: %+v", s)
	}

	if s := rel.Summarize(1, 2); s.Rated != 2 || s.Mean != 1 || s.Good != 0.5 || s.Found != 1 {
		t.Errorf("summary b at 2: %+v", s)
	}

	var b strings.Builder
	rel.Note = "On a sample."
	if err := rel.WriteMarkdown(&b); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"> On a sample.\n\n## Summary",
		"| a | 1.67 | 1.67 | 1.67 | 67% | 67% | 67% | 1 | 1 | 1 |",
		"Works shared by a and b, mean per query: 2.0 of the top 10, 2.0 of the top 30, 2.0 of the top 100.",
		"Queries where b has a better mean rating than a / a worse one / the same: 0 / 1 / 0 at the top 10,",
		"| 1 | plot | q1 | 1.67 / 1.67 / 1.67 | 1.00 / 1.00 / 1.00 | 2 |",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("markdown misses %q:\n%s", want, b.String())
		}
	}

	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	// A new judge reads the cache: only the failed ratings are asked again.
	llm.ratings["D"] = 1
	j, err = OpenJudge(llm, "m", cache)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	if _, err := RunRelevance(ctx, j, methods, queries); err != nil || llm.calls.Load() != 7 {
		t.Errorf("rerun: %d calls, %v", llm.calls.Load(), err)
	}

	if r, ok := j.Rating("q1", works("D")[0]); !ok || r != 1 {
		t.Errorf("D: %d, %v", r, ok)
	}
}

func TestJudgeErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(bad, []byte("{}\nnot json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenJudge(&fakeJudge{}, "m", bad); err == nil || !strings.Contains(err.Error(), "bad.jsonl:2") {
		t.Errorf("bad cache: %v", err)
	}

	j, err := OpenJudge(&fakeJudge{}, "m", filepath.Join(dir, "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	books := results("A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K")
	if err := j.RateAll(context.Background(), "q", books); !errors.Is(err, ErrTooManyFailures) {
		t.Errorf("all failing: %v", err)
	}
}

func TestJudgeMessage(t *testing.T) {
	b := search.Result{Title: "Rebecca", Authors: []string{"Daphne du Maurier"}, Year: 1938, Description: " A bride. "}
	want := "Search: q\n\nBook: Rebecca\nAuthors: Daphne du Maurier\nFirst published: 1938\nDescription:\nA bride."
	if got := judgeMessage("q", b); got != want {
		t.Errorf("got %q", got)
	}

	if got := judgeMessage("q", search.Result{Title: "T", Description: strings.Repeat("word ", 1000)}); len(got) > 2100 {
		t.Errorf("description not cut: %d bytes", len(got))
	}
}

// fakeQueryLLM answers with the query's words as keywords, or nothing for "empty".
type fakeQueryLLM struct{ calls int }

func (f *fakeQueryLLM) JSON(_ context.Context, system, user, _ string, _ json.RawMessage, out any) error {
	f.calls++
	if system != enrich.QueryPrompt {
		return errors.New("unexpected prompt")
	}

	words := strings.Fields(user)
	if user == "empty" {
		words = nil
	}

	k := out.(*enrich.Keywords)
	*k = enrich.Keywords{Keywords: words, Places: []string{"Amsterdam, Netherlands"}, EraLabel: "2020s"}
	if words == nil {
		*k = enrich.Keywords{}
	}

	return nil
}

func TestQueryKeywords(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "q.jsonl")
	fake := &fakeQueryLLM{}
	q, err := OpenQueryKeywords(fake, "m", path)
	if err != nil {
		t.Fatal(err)
	}

	text, took, err := q.Get(ctx, "Expat Dutch expat")
	if err != nil || text != "expat; dutch; Amsterdam, Netherlands; 2020s" {
		t.Errorf("got %q, %v, %v", text, took, err)
	}

	if _, _, err := q.Get(ctx, "empty"); !errors.Is(err, llm.ErrBadResponse) {
		t.Errorf("no keywords: %v", err)
	}

	q.Close()
	if q, err = OpenQueryKeywords(fake, "m", path); err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	if again, took2, err := q.Get(ctx, "Expat Dutch expat"); err != nil || again != text || took2 != took || fake.calls != 2 {
		t.Errorf("cached: %q, %v, %v, %d calls", again, took2, err, fake.calls)
	}
}
