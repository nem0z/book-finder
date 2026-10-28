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

	"github.com/nem0z/book-finder/internal/search"
)

// fakeJudge rates books by title ("Book A" → ratings["A"]), scores plot as min(rating, 2), mood 0 and the
// other dimensions as not asked; a missing rating fails, and a rating of 9 gets an invalid answer.
type fakeJudge struct {
	ratings map[string]int
	calls   atomic.Int64
}

func (f *fakeJudge) JSON(_ context.Context, system, user, name string, s json.RawMessage, out any) error {
	f.calls.Add(1)
	if system != prompt || name != schemaName || !json.Valid(s) {
		return errors.New("unexpected prompt")
	}

	book, _, _ := strings.Cut(strings.SplitN(user, "\nBook: Book ", 2)[1], "\n")
	r, ok := f.ratings[book]
	if !ok {
		return errors.New("no answer")
	}

	dims := []string{fmt.Sprintf(`"plot": %d`, min(r, 2)), `"mood": 0`}
	for _, d := range Dimensions[2:] {
		dims = append(dims, fmt.Sprintf(`"%s": -1`, d))
	}

	return json.Unmarshal(fmt.Appendf(nil, `{"rating": %d, "dimensions": {%s}, "reason": "r"}`, r, strings.Join(dims, ", ")), out)
}

// works returns results titled "Book <id>", by "Author", first published in 1900.
func works(ids ...string) []search.Result {
	out := make([]search.Result, len(ids))
	for i, id := range ids {
		out[i] = search.Result{ID: id, Title: "Book " + id, Authors: []string{"Author"}, Year: 1900, Description: "About " + id}
	}

	return out
}

func TestJudge(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "j.jsonl")
	llm := &fakeJudge{ratings: map[string]int{"A": 3, "B": 0, "C": 9}}
	j, err := OpenJudge(llm, "m", path)
	if err != nil {
		t.Fatal(err)
	}

	if err := j.RateAll(ctx, "q", works("A", "B", "C")); err != nil {
		t.Fatal(err)
	}

	jd, ok := j.Judgment("q", works("A")[0])
	if !ok || jd.Rating != 3 || jd.Dimensions["plot"] != 2 || jd.Dimensions["era"] != NotApplicable || jd.Reason != "r" {
		t.Errorf("A: %+v, %v", jd, ok)
	}

	if _, ok := j.Judgment("q", works("C")[0]); ok {
		t.Error("an out-of-range answer should not be kept")
	}

	if _, ok := j.Judgment("other query", works("A")[0]); ok {
		t.Error("a rating belongs to its query")
	}

	if cached, asked, failed := j.Stats(); cached != 2 || asked != 3 || failed != 1 {
		t.Errorf("stats: %d cached, %d asked, %d failed", cached, asked, failed)
	}

	j.Close()

	// A new judge reads the cache: nothing to ask again but C.
	j, err = OpenJudge(llm, "m", path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	calls := llm.calls.Load()
	if err := j.RateAll(ctx, "q", works("A", "B")); err != nil || llm.calls.Load() != calls {
		t.Errorf("cached ratings asked again: %d calls, %v", llm.calls.Load()-calls, err)
	}

	j.DryRun = true
	if err := j.RateAll(ctx, "q", works("A", "C", "D")); err != nil || j.Pending != 2 || llm.calls.Load() != calls {
		t.Errorf("dry run: %d pending, %v", j.Pending, err)
	}
}

// TestJudgeKey pins the cache key, so that a change to it, which would ask every rating again, is deliberate.
// The value is the one of the ratings cached for the benchmark in PR #53.
func TestJudgeKey(t *testing.T) {
	j := &Judge{model: "claude-haiku-5.5"}
	if got := j.key("a heist that goes wrong", works("OL1W")[0]); got != "bdec53a0993e0786e950fddc6b561415" {
		t.Errorf("key %s", got)
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

	if err := j.RateAll(context.Background(), "q", works("A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K")); !errors.Is(err, ErrTooManyFailures) {
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
