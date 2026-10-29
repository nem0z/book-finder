package index

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/embed"
	"github.com/nem0z/book-finder/internal/enrich"
)

const testDBEnv = "BOOKFINDER_TEST_DATABASE_URL"

// fakeLLM answers with each book's title as its keyword, and leaves out or fails books whose description
// says NEVER.
type fakeLLM struct{ calls atomic.Int64 }

func (f *fakeLLM) JSON(_ context.Context, _, user, _ string, _ json.RawMessage, out any) error {
	f.calls.Add(1)
	entry := func(id, part string) string {
		title, _, _ := strings.Cut(strings.SplitN(part, "Title: ", 2)[1], "\n")
		return fmt.Sprintf(`{"id": %q, "keywords": [%q], "setting_places": [], "era_from": 0, "era_to": 0, "era_label": "", "themes": [], "protagonist": ""}`, id, title)
	}

	if !strings.HasPrefix(user, "Book ") {
		if strings.Contains(user, "NEVER") {
			return errors.New("bad answer")
		}

		return json.Unmarshal([]byte(entry("", user)), out)
	}

	var entries []string
	for _, part := range strings.Split(user, "\n\n---\n\n") {
		id, _, _ := strings.Cut(strings.TrimPrefix(part, "Book "), "\n")
		if !strings.Contains(part, "NEVER") {
			entries = append(entries, entry(id, part))
		}
	}

	return json.Unmarshal([]byte(`{"books": [`+strings.Join(entries, ",")+`]}`), out)
}

// fakeEmbedder returns a unit vector and counts the texts it embedded.
type fakeEmbedder struct {
	model string
	texts atomic.Int64
}

func (f *fakeEmbedder) Model() string { return f.model }

func (f *fakeEmbedder) EmbedDocuments(_ context.Context, docs []embed.Doc) ([][]float32, error) {
	f.texts.Add(int64(len(docs)))
	vecs := make([][]float32, len(docs))
	for i := range docs {
		vecs[i] = make([]float32, embed.Dims)
		vecs[i][0] = 1
	}

	return vecs, nil
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(testDBEnv)
	if url == "" {
		t.Skipf("%s not set", testDBEnv)
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, "TRUNCATE works, work_authors, authors, work_keywords, work_keyword_embeddings"); err != nil {
		t.Fatal(err)
	}

	return pool
}

// insertWorks adds works with these descriptions, padded to be long enough unless they say SHORT, the
// first most rated.
func insertWorks(t *testing.T, pool *pgxpool.Pool, descriptions ...string) {
	t.Helper()
	for i, d := range descriptions {
		if !strings.Contains(d, "SHORT") {
			d += strings.Repeat(" story", MinDescription/6)
		}

		if _, err := pool.Exec(context.Background(), `INSERT INTO works (id, title, author_names, description, rating_count)
			VALUES ($1, $2, '{Ann}', $3, $4)`, fmt.Sprintf("OL%dW", i+1), fmt.Sprintf("Title %d", i+1), d, 100-i); err != nil {
			t.Fatal(err)
		}
	}
}

func quiet() Options { return Options{Concurrency: 2, PerRequest: 2, ProgressInterval: -1} }

func TestRun(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	insertWorks(t, pool, "one", "two", "SHORT", "see https://example.com", "five", "NEVER", "seven")

	llm, emb := &fakeLLM{}, &fakeEmbedder{model: "e1"}
	opts := quiet()
	opts.DryRun = true
	s, err := Run(ctx, pool, llm, "m", emb, opts)
	if err != nil || s.Considered != 5 || s.ToExtract != 5 || llm.calls.Load() != 0 {
		t.Fatalf("dry run: %+v, %v, %d calls", s, err, llm.calls.Load())
	}

	// NEVER fails every attempt; the others are extracted and embedded.
	s, err = Run(ctx, pool, llm, "m", emb, quiet())
	if err != nil || s.Extracted != 4 || s.Failed != 1 || s.Embedded != 4 || emb.texts.Load() != 4 || s.Requests == 0 {
		t.Fatalf("run: %+v, %v", s, err)
	}

	var kwHash, vecHash, model string
	if err := pool.QueryRow(ctx, `SELECT k.content_hash, e.content_hash, e.model FROM work_keywords k
		JOIN work_keyword_embeddings e USING (work_id) WHERE work_id = 'OL1W'`).Scan(&kwHash, &vecHash, &model); err != nil {
		t.Fatal(err)
	}

	book := enrich.Book{ID: "OL1W", Title: "Title 1", Authors: []string{"Ann"}, Description: "one" + strings.Repeat(" story", MinDescription/6)}
	if kwHash != enrich.ContentHash("m", book) || vecHash != embed.KeywordHash("e1", "title 1") || model != "e1" {
		t.Errorf("hashes: keywords %s, vector %s by %s", kwHash, vecHash, model)
	}

	// Only the failed work is left, and fails again: 3 rounds of one book. A new embedding model re-embeds the
	// others without asking the LLM.
	calls := llm.calls.Load()
	emb = &fakeEmbedder{model: "e2"}
	s, err = Run(ctx, pool, llm, "m", emb, quiet())
	if err != nil || s.ToExtract != 1 || s.ToEmbed != 4 || s.Embedded != 4 || llm.calls.Load()-calls != 3 || s.Failed != 1 {
		t.Errorf("new embedding model: %+v, %v, %d calls", s, err, llm.calls.Load()-calls)
	}

	if _, err := pool.Exec(ctx, "UPDATE works SET description = replace(description, 'NEVER', 'six') WHERE id = 'OL6W'"); err != nil {
		t.Fatal(err)
	}

	// A work gone from the catalogue loses its keywords and vector, except on a limited run.
	if _, err := pool.Exec(ctx, "DELETE FROM works WHERE id = 'OL7W'"); err != nil {
		t.Fatal(err)
	}

	limited := quiet()
	limited.Limit = 2
	if s, err := Run(ctx, pool, llm, "m", emb, limited); err != nil || s.Considered != 2 || s.Pruned != 0 {
		t.Errorf("limited run: %+v, %v", s, err)
	}

	s, err = Run(ctx, pool, llm, "m", emb, quiet())
	if err != nil || s.Extracted != 1 || s.Failed != 0 || s.Pruned != 2 || s.UpToDate != 3 {
		t.Errorf("last run: %+v, %v", s, err)
	}

	if s, _ := Run(ctx, pool, llm, "m", emb, Options{DryRun: true, ProgressInterval: -1}); s.ToExtract+s.ToEmbed != 0 || s.UpToDate != 4 {
		t.Errorf("all done: %+v", s)
	}
}

func TestRunTooManyFailures(t *testing.T) {
	pool := testPool(t)
	descriptions := make([]string, 12)
	for i := range descriptions {
		descriptions[i] = "NEVER"
	}

	insertWorks(t, pool, descriptions...)
	opts := quiet()
	opts.Attempts = 1
	if _, err := Run(context.Background(), pool, &fakeLLM{}, "m", &fakeEmbedder{model: "e"}, opts); !errors.Is(err, ErrTooManyFailures) {
		t.Errorf("all failing: %v", err)
	}
}
