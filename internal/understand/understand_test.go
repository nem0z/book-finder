package understand

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nem0z/book-finder/internal/search"
)

func TestInterpret(t *testing.T) {
	var got chatRequest
	var auth string
	status := http.StatusOK
	reply := chatReply(`{"description":"An expat settles in Amsterdam.","min_year":2020,"max_year":0,"min_pages":0,"max_pages":300}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}

		auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}

		w.WriteHeader(status)
		w.Write([]byte(reply)) //nolint:errcheck
	}))
	defer srv.Close()

	c := New(srv.URL+"/v1/", "m", "key")
	c.now = func() time.Time { return time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC) }
	in, err := c.Interpret(context.Background(), "a book about an expat in amsterdam (2020+), under 300 pages")
	want := search.Interpretation{
		Filters:     search.Filters{MinYear: 2020, MaxPages: 300},
		Description: "An expat settles in Amsterdam.",
	}
	if err != nil || in != want {
		t.Fatalf("got %+v, %v", in, err)
	}

	if auth != "Bearer key" || got.Model != "m" || len(got.Messages) != 2 || got.Messages[1].Content != "a book about an expat in amsterdam (2020+), under 300 pages" {
		t.Errorf("request: %q %+v", auth, got)
	}

	if !strings.Contains(got.Messages[0].Content, "The current year is 2026.") {
		t.Errorf("system prompt without the year: %s", got.Messages[0].Content)
	}

	if f := got.ResponseFormat; f.Type != "json_schema" || !f.JSONSchema.Strict || !json.Valid(f.JSONSchema.Schema) {
		t.Errorf("response format: %+v", f)
	}

	for _, bad := range []string{`{"choices":[]}`, chatReply(`not json`), `{"choices":[{"message":{"content":"","refusal":"no"}}]}`, `not json`} {
		reply = bad
		if _, err := c.Interpret(context.Background(), "q"); !errors.Is(err, ErrBadResponse) {
			t.Errorf("%s: got %v", bad, err)
		}
	}

	status, reply = http.StatusUnauthorized, `{"error":{"message":"invalid key"}}`
	if _, err := c.Interpret(context.Background(), "q"); !errors.Is(err, ErrBadResponse) || !strings.Contains(err.Error(), "invalid key") {
		t.Errorf("401: %v", err)
	}
}

func TestInterpretNoKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			t.Errorf("Authorization %q without a key", a)
		}

		w.Write([]byte(chatReply(`{"description":"","min_year":0,"max_year":0,"min_pages":0,"max_pages":0}`))) //nolint:errcheck
	}))
	defer srv.Close()

	in, err := New(srv.URL, "m", "").Interpret(context.Background(), "a man wakes up as an insect")
	if err != nil || in != (search.Interpretation{Description: "a man wakes up as an insect"}) {
		t.Errorf("got %+v, %v", in, err)
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
	if it, _, err := FromConfig("", "", "", ""); it != nil || err != nil {
		t.Errorf("off: %v, %v", it, err)
	}

	if _, _, err := FromConfig("http://llm", "", "", "5s"); !errors.Is(err, ErrNoModel) {
		t.Errorf("no model: %v", err)
	}

	for _, bad := range []string{"", "5", "-1s"} {
		if _, _, err := FromConfig("http://llm", "m", "", bad); err == nil {
			t.Errorf("timeout %q: no error", bad)
		}
	}

	it, d, err := FromConfig("http://llm/", "m", "k", "30s")
	if c, ok := it.(*Client); !ok || err != nil || d != 30*time.Second || c.baseURL != "http://llm" || c.tokens != apiKey("k") {
		t.Errorf("got %+v, %v, %v", it, d, err)
	}

	t.Setenv("IDENTITY_ENDPOINT", "http://identity")
	t.Setenv("IDENTITY_HEADER", "secret")
	t.Setenv("AZURE_CLIENT_ID", "id")
	it, _, _ = FromConfig("http://llm", "m", "", "5s")
	if mi, ok := it.(*Client).tokens.(*managedIdentity); !ok || mi.endpoint != "http://identity" || mi.secret != "secret" || mi.clientID != "id" {
		t.Errorf("managed identity: %+v", it.(*Client).tokens)
	}

	if it, _, _ = FromConfig("http://llm", "m", "k", "5s"); it.(*Client).tokens != apiKey("k") {
		t.Errorf("a key should win over the managed identity: %+v", it.(*Client).tokens)
	}
}

func chatReply(content string) string {
	b, _ := json.Marshal(content)
	return `{"choices":[{"index":0,"message":{"role":"assistant","content":` + string(b) + `}}]}`
}
