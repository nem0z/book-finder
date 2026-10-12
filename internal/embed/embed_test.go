package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeOllama(t *testing.T, dims int, check func(req map[string]any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if check != nil {
			check(req)
		}
		input := req["input"].([]any)
		out := make([][]float32, len(input))
		for i := range out {
			out[i] = make([]float32, dims)
			out[i][i%dims] = 1
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": out}) //nolint:errcheck
	}))
}

func TestOllamaEmbeddingGemmaPrompts(t *testing.T) {
	var got []string
	var numCtx float64
	srv := fakeOllama(t, Dims, func(req map[string]any) {
		got = nil
		for _, in := range req["input"].([]any) {
			got = append(got, in.(string))
		}
		numCtx = req["options"].(map[string]any)["num_ctx"].(float64)
	})
	defer srv.Close()

	o := NewOllama(srv.URL+"/", "embeddinggemma")
	vecs, err := o.EmbedDocuments(context.Background(), []Doc{{Title: "Dune", Text: "Desert planet."}, {Text: "No title."}})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || len(vecs[0]) != Dims {
		t.Fatalf("vectors: %d x %d", len(vecs), len(vecs[0]))
	}
	if got[0] != "title: Dune | text: Desert planet." || got[1] != "title: none | text: No title." {
		t.Errorf("document prompts: %q", got)
	}
	if numCtx != 2048 {
		t.Errorf("num_ctx: %v", numCtx)
	}
	if _, err := o.EmbedQuery(context.Background(), "spice"); err != nil {
		t.Fatal(err)
	}
	if got[0] != "task: search result | query: spice" {
		t.Errorf("query prompt: %q", got[0])
	}
}

func TestOllamaErrors(t *testing.T) {
	wrongDims := fakeOllama(t, 384, nil)
	defer wrongDims.Close()
	if _, err := NewOllama(wrongDims.URL, "embeddinggemma").EmbedQuery(context.Background(), "q"); err == nil ||
		!strings.Contains(err.Error(), "384 dimensions") {
		t.Errorf("want dimension error, got %v", err)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model \"x\" not found"}`, http.StatusNotFound)
	}))
	defer failing.Close()
	if _, err := NewOllama(failing.URL, "x").EmbedQuery(context.Background(), "q"); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Errorf("want HTTP error, got %v", err)
	}
}

func TestVectorLiteral(t *testing.T) {
	if got := VectorLiteral([]float32{0.5, -1, 0.25}); got != "[0.5,-1,0.25]" {
		t.Errorf("got %s", got)
	}
}

func TestDocument(t *testing.T) {
	d := Document(Book{
		Title: "The Snowman", Subtitle: "A Harry Hole Thriller", Authors: []string{"Jo Nesbø"},
		Genres: []string{"Fiction", "Thriller"}, Subjects: []string{"fiction", "Oslo (Norway)", "Thriller"},
		Description: "A killer builds snowmen.",
	})
	if d.Title != "The Snowman: A Harry Hole Thriller" {
		t.Errorf("title: %q", d.Title)
	}
	want := "By Jo Nesbø\nTopics: Thriller; Oslo (Norway)\n\nA killer builds snowmen."
	if d.Text != want {
		t.Errorf("text:\n%q\nwant\n%q", d.Text, want)
	}

	long := Document(Book{Title: "T", Description: strings.Repeat("word ", 2000)})
	if len(long.Text) > maxDescriptionChars {
		t.Errorf("description not truncated: %d", len(long.Text))
	}

	if ContentHash("m", d) != ContentHash("m", d) || ContentHash("m", d) == ContentHash("other", d) {
		t.Error("hash must be stable and depend on the model")
	}
}
