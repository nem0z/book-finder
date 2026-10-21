package understand

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nem0z/book-finder/internal/llm"
	"github.com/nem0z/book-finder/internal/search"
)

// fakeLLM answers every request with reply, and remembers the last request.
type fakeLLM struct {
	reply              string
	err                error
	system, user, name string
	schema             json.RawMessage
}

func (f *fakeLLM) JSON(_ context.Context, system, user, schemaName string, schema json.RawMessage, out any) error {
	f.system, f.user, f.name, f.schema = system, user, schemaName, schema
	if f.err != nil {
		return f.err
	}

	return json.Unmarshal([]byte(f.reply), out)
}

func TestInterpret(t *testing.T) {
	f := &fakeLLM{reply: `{"description":" An expat settles in Amsterdam. ","min_year":2020,"max_year":0,"min_pages":0,"max_pages":300}`}
	in := New(f)
	in.now = func() time.Time { return time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC) }

	query := "a book about an expat in amsterdam (2020+), under 300 pages"
	got, err := in.Interpret(context.Background(), query)
	want := search.Interpretation{
		Filters:     search.Filters{MinYear: 2020, MaxPages: 300},
		Description: "An expat settles in Amsterdam.",
	}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}

	if f.user != query || f.name != schemaName || !json.Valid(f.schema) || !strings.Contains(f.system, "The current year is 2026.") {
		t.Errorf("request: %q %q %s\n%s", f.user, f.name, f.schema, f.system)
	}

	f.reply = `{"description":"","min_year":0,"max_year":0,"min_pages":0,"max_pages":0}`
	if got, err := in.Interpret(context.Background(), "a man wakes up as an insect"); err != nil || got.Description != "a man wakes up as an insect" {
		t.Errorf("empty description: %+v, %v", got, err)
	}

	f.err = errors.New("down")
	if _, err := in.Interpret(context.Background(), "q"); !errors.Is(err, f.err) {
		t.Errorf("LLM error: %v", err)
	}
}

func TestInterpretation(t *testing.T) {
	tests := []struct {
		name string
		a    queryAnswer
		want search.Filters
	}{
		{"none", queryAnswer{}, search.Filters{}},
		{"all", queryAnswer{MinYear: 1960, MaxYear: 1969, MinPages: 100, MaxPages: 200}, search.Filters{MinYear: 1960, MaxYear: 1969, MinPages: 100, MaxPages: 200}},
		{"next year", queryAnswer{MinYear: 2027}, search.Filters{MinYear: 2027}},
		{"future", queryAnswer{MinYear: 2030, MaxYear: 2040}, search.Filters{}},
		{"negative", queryAnswer{MinYear: -5, MaxPages: -1}, search.Filters{}},
		{"too long", queryAnswer{MinPages: 50_000}, search.Filters{}},
		{"min over max", queryAnswer{MinYear: 2000, MaxYear: 1990, MinPages: 300}, search.Filters{MinPages: 300}},
	}

	for _, tt := range tests {
		tt.a.Description = "d"
		if got := tt.a.interpretation("from 1960, 100 pages", 2026); got.Filters != tt.want || got.Description != "d" {
			t.Errorf("%s: got %+v", tt.name, got)
		}
	}

	all := queryAnswer{Description: "d", MinYear: 1960, MaxPages: 200}
	for query, want := range map[string]search.Filters{
		"set in Victorian London":                  {},
		"a novel from the 1960s":                   {MinYear: 1960},
		"something from the '60s":                  {MinYear: 1960},
		"a 19th-century novel":                     {MinYear: 1960},
		"modern amsterdam (2020+)":                 {MinYear: 1960},
		"a short book of 200 Pages or less":        {MaxPages: 200},
		"under 200 pages, published 1960 or later": {MinYear: 1960, MaxPages: 200},
	} {
		if got := all.interpretation(query, 2026); got.Filters != want {
			t.Errorf("%q: got %+v", query, got.Filters)
		}
	}
}

func TestFromConfig(t *testing.T) {
	if in, _, err := FromConfig(llm.Config{}, ""); in != nil || err != nil {
		t.Errorf("off: %v, %v", in, err)
	}

	ollama := llm.Config{Provider: llm.ProviderOllama, URL: "http://o", Model: "m"}
	for _, bad := range []string{"", "5", "-1s"} {
		if _, _, err := FromConfig(ollama, bad); !errors.Is(err, llm.ErrConfig) {
			t.Errorf("timeout %q: %v", bad, err)
		}
	}

	if _, _, err := FromConfig(llm.Config{Provider: "nope", Model: "m"}, "5s"); !errors.Is(err, llm.ErrConfig) {
		t.Errorf("bad provider: %v", err)
	}

	in, d, err := FromConfig(ollama, "30s")
	if err != nil || in == nil || d != 30*time.Second || in.Close() != nil {
		t.Errorf("got %v, %v, %v", in, d, err)
	}

	if err := (*Interpreter)(nil).Close(); err != nil {
		t.Errorf("nil close: %v", err)
	}
}
