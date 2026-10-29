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

func TestTopics(t *testing.T) {
	got := Topics([]string{"Fiction", "Thriller"}, []string{"thriller", "Oslo", " "})
	if strings.Join(got, "; ") != "Thriller; Oslo" {
		t.Errorf("topics: %q", got)
	}
}

func TestVectorLiteral(t *testing.T) {
	if got := VectorLiteral([]float32{0.5, -1, 0}); got != "[0.5,-1,0]" {
		t.Errorf("got %q", got)
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
