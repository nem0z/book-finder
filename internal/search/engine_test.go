package search

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nem0z/book-finder/internal/embed"
)

type fakeInterpreter struct {
	in    Interpretation
	err   error
	delay time.Duration
}

// recordingEmbedder remembers the text it embedded.
type recordingEmbedder struct{ text string }

func (f fakeInterpreter) Interpret(ctx context.Context, _ string) (Interpretation, error) {
	select {
	case <-time.After(f.delay):
		return f.in, f.err
	case <-ctx.Done():
		return Interpretation{}, ctx.Err()
	}
}

func (r *recordingEmbedder) Model() string { return "fake" }

func (r *recordingEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	r.text = text
	return axis(1, 1), nil
}

func TestInterpret(t *testing.T) {
	ctx := context.Background()
	caller := Filters{MaxYear: 1950, OriginalLanguage: "eng"}
	interpreted := Interpretation{Filters: Filters{MinYear: 2020, MaxPages: 300}, Text: " expat; amsterdam "}

	tests := []struct {
		name string
		it   Interpreter
		want Interpretation
		ok   bool
		warn bool
	}{
		{"none", nil, Interpretation{Filters: caller, Text: "q"}, false, false},
		{"merged", fakeInterpreter{in: interpreted}, Interpretation{Filters: Filters{MaxYear: 1950, MaxPages: 300, OriginalLanguage: "eng"}, Text: "expat; amsterdam"}, true, false},
		{"empty text", fakeInterpreter{in: Interpretation{Filters: Filters{MinPages: 50}}}, Interpretation{Filters: Filters{MaxYear: 1950, MinPages: 50, OriginalLanguage: "eng"}, Text: "q"}, true, false},
		{"error", fakeInterpreter{err: errors.New("down")}, Interpretation{Filters: caller, Text: "q"}, false, true},
		{"timeout", fakeInterpreter{in: interpreted, delay: time.Second}, Interpretation{Filters: caller, Text: "q"}, false, true},
	}

	for _, tt := range tests {
		var logs bytes.Buffer
		e := &Engine{Interpreter: tt.it, InterpretTimeout: 10 * time.Millisecond, Log: slog.New(slog.NewTextHandler(&logs, nil))}
		if got, ok := e.interpret(ctx, "q", caller); got != tt.want || ok != tt.ok {
			t.Errorf("%s: got %+v, %v", tt.name, got, ok)
		}

		if warned := strings.Contains(logs.String(), "not interpreted"); warned != tt.warn {
			t.Errorf("%s: logs %q", tt.name, logs.String())
		}
	}
}

func TestMerge(t *testing.T) {
	interpreted := Filters{MinYear: 2020, MaxYear: 2024, MinPages: 100, MaxPages: 300, OriginalLanguage: "fre"}
	if got := merge(Filters{}, interpreted); got != interpreted {
		t.Errorf("no caller filters: %+v", got)
	}

	caller := Filters{MinYear: 1900, MaxPages: 150, OriginalLanguage: "eng"}
	if got := merge(caller, interpreted); got != caller {
		t.Errorf("caller filters: %+v", got)
	}
}

func TestEngineSearchInterpreted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO works (id, title, description, first_publish_year) VALUES
		('OL1W', 'Old', 'd', 1950), ('OL2W', 'New', 'd', 2021)`); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"OL1W", "OL2W"} {
		if _, err := pool.Exec(ctx, "INSERT INTO work_keyword_embeddings (work_id, model, content_hash, embedding) VALUES ($1, 'fake', 'h', $2::halfvec)",
			id, embed.VectorLiteral(axis(1, 1))); err != nil {
			t.Fatal(err)
		}
	}

	emb, rr := &recordingEmbedder{}, &fakeReranker{scores: []float64{1}}
	e := &Engine{Pool: pool, Embedder: emb, Reranker: rr,
		Interpreter: fakeInterpreter{in: Interpretation{Filters: Filters{MinYear: 2020}, Text: "expat; amsterdam"}}}
	res, in, err := e.SearchInterpreted(ctx, "I want a recent book", Filters{}, 10)
	if err != nil || len(res) != 1 || res[0].ID != "OL2W" || in == nil || in.Filters.MinYear != 2020 || in.Text != "expat; amsterdam" {
		t.Fatalf("got %+v, %+v, %v", res, in, err)
	}

	if emb.text != "expat; amsterdam" || rr.query != "I want a recent book" {
		t.Errorf("embedded %q, re-ranked with %q", emb.text, rr.query)
	}

	e.Interpreter = fakeInterpreter{err: errors.New("down")}
	e.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	rr.scores = []float64{1, 2}
	if res, in, err := e.SearchInterpreted(ctx, "I want a recent book", Filters{}, 10); err != nil || len(res) != 2 || in != nil {
		t.Errorf("fallback: %+v, %+v, %v", res, in, err)
	}

	if _, _, err := e.SearchInterpreted(ctx, " ", Filters{}, 10); !errors.Is(err, ErrEmptyQuery) {
		t.Errorf("empty query: %v", err)
	}
}
