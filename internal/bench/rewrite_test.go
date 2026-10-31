package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nem0z/book-finder/internal/enrich"
	"github.com/nem0z/book-finder/internal/llm"
	"github.com/nem0z/book-finder/internal/search"
)

// fakeRewriter answers the query interpretation and keyword prompts, and counts the calls.
type fakeRewriter struct {
	keywords []string
	calls    int
}

// recordingRR remembers the query texts it re-ranked with.
type recordingRR struct{ queries []string }

func (f *fakeRewriter) JSON(_ context.Context, system, user, _ string, _ json.RawMessage, out any) error {
	f.calls++
	if system == enrich.QueryPrompt {
		if f.keywords == nil {
			return json.Unmarshal([]byte(`{"keywords":[],"setting_places":[],"era_from":0,"era_to":0,"era_label":"","themes":[],"protagonist":""}`), out)
		}

		return json.Unmarshal([]byte(`{"keywords":`+mustJSON(f.keywords)+`,"setting_places":["Amsterdam, Netherlands"],"era_from":0,"era_to":0,"era_label":"2020s","themes":[],"protagonist":""}`), out)
	}

	return json.Unmarshal([]byte(`{"description":"An expat in Amsterdam.","min_year":2020,"max_year":0,"min_pages":0,"max_pages":0}`), out)
}

func (r *recordingRR) Scores(_ context.Context, query string, texts []string) ([]float64, error) {
	r.queries = append(r.queries, query)
	return make([]float64, len(texts)), nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestRewriter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rw.jsonl")
	f := &fakeRewriter{keywords: []string{"Expat", "amsterdam", "expat"}}
	r, err := OpenRewriter(f, "m", path)
	if err != nil {
		t.Fatal(err)
	}

	query := "a book about an expat in modern amsterdam (2020+)"
	rw, err := r.Get(ctx, query)
	want := Rewrite{Query: query, Description: "An expat in Amsterdam.", Filters: search.Filters{MinYear: 2020},
		Keywords: "expat; amsterdam; Amsterdam, Netherlands; 2020s"}
	rw.Key = ""
	if err != nil || rw != want {
		t.Fatalf("got %+v, %v", rw, err)
	}

	r.Close()
	if r, err = OpenRewriter(f, "m", path); err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if _, err := r.Get(ctx, query); err != nil || f.calls != 2 {
		t.Errorf("cached: %v after %d calls", err, f.calls)
	}

	f.keywords = nil
	if _, err := r.Get(ctx, "another query"); !errors.Is(err, llm.ErrBadResponse) {
		t.Errorf("no keywords: %v", err)
	}
}

func TestRerankQueries(t *testing.T) {
	ev, _ := testEval(t)
	rr := &recordingRR{}
	ev.RerankQueries = []string{"r1", "r2"}
	if err := ev.RerankPool(context.Background(), rr); err != nil || !slices.Equal(rr.queries, []string{"r1", "r2"}) {
		t.Errorf("re-ranked with %v, %v", rr.queries, err)
	}
}

func TestWriteJSON(t *testing.T) {
	ev, _ := testEval(t)
	var b bytes.Buffer
	if err := ev.WriteJSON(&b); err != nil {
		t.Fatal(err)
	}

	var out []queryResults
	if err := json.Unmarshal(b.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	// q1's pool is C, A; re-ranking reverses it, then come B, D, E, F.
	q1 := out[0]
	if q1.Query != "q1" || q1.Category != "plot" || len(q1.Pool) != 2 || q1.Pool[0].ID != "C" || q1.Pool[1].Rating != 3 ||
		len(q1.Top10) != 6 || q1.Top10[0].ID != "A" || q1.NDCG10 <= 0 {
		t.Errorf("q1: %+v", q1)
	}

	if !strings.Contains(b.String(), `"query": "q2"`) {
		t.Errorf("no q2: %s", b.String())
	}
}
