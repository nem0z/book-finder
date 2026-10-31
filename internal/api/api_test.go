package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/nem0z/book-finder/internal/search"
)

type fakeSearcher struct {
	results []search.Result
	in      *search.Interpretation
	err     error
	query   string
	limit   int
}

func (f *fakeSearcher) SearchInterpreted(_ context.Context, query string, _ search.Filters, limit int) ([]search.Result, *search.Interpretation, error) {
	f.query, f.limit = query, limit
	return f.results, f.in, f.err
}

func get(t *testing.T, h http.Handler, target string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: body %q: %v", target, rec.Body.String(), err)
	}

	return rec, body
}

func TestSearch(t *testing.T) {
	s := &fakeSearcher{results: []search.Result{
		{ID: "OL59048W", Title: "The Remains of the Day", Authors: []string{"Kazuo Ishiguro"}, Year: 1989, Pages: 245,
			Description: "A butler.", Tags: []string{"Butlers"}, CoverID: 42},
		{ID: "OL1W", Title: "No cover"},
	}}
	h := NewHandler(s, slog.New(slog.NewTextHandler(io.Discard, nil)))

	rec, body := get(t, h, "/search?q="+url.QueryEscape("  an aging butler  "))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, content type %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	if s.query != "an aging butler" || s.limit != ResultLimit || body["query"] != "an aging butler" {
		t.Errorf("query %q, limit %d, body %v", s.query, s.limit, body)
	}

	results := body["results"].([]any)
	first := results[0].(map[string]any)
	if first["cover_url"] != "https://covers.openlibrary.org/b/id/42-M.jpg" || first["url"] != "https://openlibrary.org/works/OL59048W" ||
		first["pages"] != 245.0 || first["year"] != 1989.0 {
		t.Errorf("first: %v", first)
	}

	second := results[1].(map[string]any)
	if _, ok := second["cover_url"]; ok || second["authors"] == nil || second["tags"] == nil {
		t.Errorf("second should have no cover and empty lists: %v", second)
	}

	if _, ok := body["interpretation"]; ok {
		t.Errorf("interpretation without an LLM: %v", body["interpretation"])
	}
}

func TestSearchInterpretation(t *testing.T) {
	s := &fakeSearcher{
		results: []search.Result{{ID: "OL1W", Title: "Two Blankets, Three Sheets"}},
		in:      &search.Interpretation{Text: "expat; amsterdam; netherlands", Filters: search.Filters{MinYear: 2020, MaxPages: 300}},
	}
	rec := httptest.NewRecorder()
	NewHandler(s, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search?q=amsterdam", nil))

	want := `"interpretation":{"keywords":"expat; amsterdam; netherlands","filters":{"min_year":2020,"max_pages":300}}`
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("got %s, want %s", rec.Body.String(), want)
	}
}

func TestSearchErrors(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for target, want := range map[string]int{
		"/search":       http.StatusBadRequest,
		"/search?q=%20": http.StatusBadRequest,
		"/search?q=" + strings.Repeat("a", maxQueryBytes+1): http.StatusBadRequest,
	} {
		if rec, body := get(t, NewHandler(&fakeSearcher{}, log), target); rec.Code != want || body["error"] == "" {
			t.Errorf("%.40s: %d %v", target, rec.Code, body)
		}
	}

	rec, body := get(t, NewHandler(&fakeSearcher{err: errors.New("dial tcp 10.0.0.1:5432: refused")}, log), "/search?q=x")
	if rec.Code != http.StatusInternalServerError || body["error"] != "search failed" {
		t.Errorf("search error: %d %v", rec.Code, body)
	}

	rec = httptest.NewRecorder()
	NewHandler(&fakeSearcher{}, log).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/search?q=x", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}
