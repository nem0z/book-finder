package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDocument(t *testing.T) {
	d := Document(Book{
		Title: "The Thirst", Subtitle: "A Harry Hole Novel", Authors: []string{"Jo Nesbø"},
		Genres: []string{"Fiction", "Thriller"}, Subjects: []string{"thriller", "Oslo", " "},
		Description: "Harry Hole hunts a killer.",
	})

	if d.Title != "The Thirst: A Harry Hole Novel" {
		t.Errorf("title: %q", d.Title)
	}

	if want := "By Jo Nesbø\nTopics: Thriller; Oslo\n\nHarry Hole hunts a killer."; d.Text != want {
		t.Errorf("text: %q", d.Text)
	}

	if d := Document(Book{Title: "T"}); d.Text != "" {
		t.Errorf("empty book text: %q", d.Text)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("one two three", 9); got != "one two" {
		t.Errorf("word boundary: %q", got)
	}

	if got := truncate(strings.Repeat("a", 20), 10); got != strings.Repeat("a", 10) {
		t.Errorf("no boundary: %q", got)
	}

	if got := truncate("ééééé", 3); got != "é" {
		t.Errorf("utf-8: %q", got)
	}
}

func TestContentHash(t *testing.T) {
	d := Doc{Title: "T", Text: "x"}
	if ContentHash("m", d) == ContentHash("m2", d) || ContentHash("m", d) == ContentHash("m", Doc{Title: "T", Text: "y"}) {
		t.Error("hash should change with the model and the text")
	}
}

func TestOllama(t *testing.T) {
	var got embedRequest
	dims := Dims
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}

		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}

		resp := embedResponse{}
		for range got.Input {
			resp.Embeddings = append(resp.Embeddings, make([]float32, dims))
		}

		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer srv.Close()

	o := NewOllama(srv.URL+"/", DefaultModel)
	vecs, err := o.EmbedDocuments(context.Background(), []Doc{{Title: "T", Text: "x"}, {Text: "y"}})
	if err != nil || len(vecs) != 2 {
		t.Fatalf("documents: %d vectors, %v", len(vecs), err)
	}

	want := []string{"title: T | text: x", "title: none | text: y"}
	if got.Model != DefaultModel || !got.Truncate || got.Options.NumCtx != contextTokens || strings.Join(got.Input, "\n") != strings.Join(want, "\n") {
		t.Errorf("request: %+v", got)
	}

	if _, err := o.EmbedQuery(context.Background(), "q"); err != nil || got.Input[0] != "task: search result | query: q" {
		t.Errorf("query: %q, %v", got.Input, err)
	}

	dims = 3
	if _, err := o.EmbedQuery(context.Background(), "q"); !errors.Is(err, ErrBadResponse) {
		t.Errorf("wrong size: %v", err)
	}

	if _, err := NewOllama(srv.URL+"/missing", DefaultModel).EmbedQuery(context.Background(), "q"); !errors.Is(err, ErrBadResponse) {
		t.Errorf("404: %v", err)
	}
}
