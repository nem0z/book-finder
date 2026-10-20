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
		if got := tt.a.interpretation("q", 2026); got.Filters != tt.want || got.Description != "d" {
			t.Errorf("%s: got %+v", tt.name, got)
		}
	}
}

func chatReply(content string) string {
	b, _ := json.Marshal(content)
	return `{"choices":[{"index":0,"message":{"role":"assistant","content":` + string(b) + `}}]}`
}
