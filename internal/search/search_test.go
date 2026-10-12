package search

import (
	"context"
	"hash/fnv"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/embed"
)

// bagOfWords is a deterministic stand-in for a real model: texts sharing words get similar vectors.
type bagOfWords struct{}

func (bagOfWords) Model() string { return "test-bag-of-words" }

func (bagOfWords) vec(text string) []float32 {
	v := make([]float32, embed.Dims)
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
		h := fnv.New32a()
		h.Write([]byte(w)) //nolint:errcheck
		v[h.Sum32()%embed.Dims]++
	}
	var norm float64
	for _, x := range v {
		norm += float64(x * x)
	}
	if norm > 0 {
		for i := range v {
			v[i] /= float32(math.Sqrt(norm))
		}
	} else {
		v[0] = 1
	}
	return v
}

func (b bagOfWords) EmbedDocuments(_ context.Context, docs []embed.Doc) ([][]float32, error) {
	out := make([][]float32, len(docs))
	for i, d := range docs {
		out[i] = b.vec(d.Title + " " + d.Text)
	}
	return out, nil
}

func (b bagOfWords) EmbedQuery(_ context.Context, q string) ([]float32, error) { return b.vec(q), nil }

func ids(rs []Result) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

// Shares the database with the ingest test; CI runs packages serially (-p 1).
func TestEmbedAndSearch(t *testing.T) {
	url := os.Getenv("BOOKFINDER_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("BOOKFINDER_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE works, work_authors, editions, authors, work_embeddings;
		INSERT INTO works (id, title, description, first_publish_year, page_count, original_language, author_names, want_to_read) VALUES
		('W1', 'The Snowman', 'A detective hunts a serial killer in snowy Oslo.', 2007, 400, 'nor', '{Jo Nesbø}', 10),
		('W2', 'Bake Off Love', 'A cozy romance in a small bakery with cakes and coffee.', 2019, 300, 'eng', '{}', 5),
		('W3', 'Space Heist', 'A crew plans a daring heist aboard a generation starship.', 1985, 250, 'eng', '{}', 1),
		('W4', 'No Description', NULL, 2000, 100, 'eng', '{}', 0)`); err != nil {
		t.Fatal(err)
	}

	emb := bagOfWords{}
	run := func() embed.Stats {
		t.Helper()
		s, err := embed.Run(ctx, pool, emb, embed.Options{BatchSize: 2, Workers: 2, PageSize: 2, ProgressInterval: -1})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	if s := run(); s.Considered != 3 || s.Embedded != 3 {
		t.Fatalf("first run: %+v", s)
	}
	if s := run(); s.Embedded != 0 || s.UpToDate != 3 {
		t.Fatalf("second run should be a no-op: %+v", s)
	}
	if _, err := pool.Exec(ctx, "UPDATE works SET description = 'A cozy romance between two bakers.' WHERE id = 'W2'"); err != nil {
		t.Fatal(err)
	}
	if s := run(); s.Embedded != 1 || s.UpToDate != 2 {
		t.Fatalf("changed text should be re-embedded: %+v", s)
	}

	find := func(q string, f Filters) []string {
		t.Helper()
		rs, err := Semantic(ctx, pool, emb, q, f, 10)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(rs); i++ {
			if rs[i].Similarity > rs[i-1].Similarity {
				t.Errorf("results not sorted by similarity: %v", rs)
			}
		}
		return ids(rs)
	}

	if got := find("detective killer in snowy oslo", Filters{}); len(got) != 3 || got[0] != "W1" {
		t.Errorf("semantic: %v", got)
	}
	if got := find("detective killer in snowy oslo", Filters{MaxYear: 2000}); !slices.Equal(got, []string{"W3"}) {
		t.Errorf("year filter: %v", got)
	}
	if got := find("detective", Filters{OriginalLanguage: "eng", MinPages: 260}); !slices.Equal(got, []string{"W2"}) {
		t.Errorf("language + pages filter: %v", got)
	}
	if got := find("detective", Filters{ExcludeIDs: []string{"W1", "W2"}}); !slices.Equal(got, []string{"W3"}) {
		t.Errorf("exclude: %v", got)
	}

	if _, err := pool.Exec(ctx, "DELETE FROM works WHERE id = 'W3'"); err != nil {
		t.Fatal(err)
	}
	if s := run(); s.Pruned != 1 {
		t.Errorf("embedding of deleted work should be pruned: %+v", s)
	}

	if _, err := pool.Exec(ctx, "TRUNCATE work_embeddings"); err != nil {
		t.Fatal(err)
	}
	s, err := embed.Run(ctx, pool, emb, embed.Options{Limit: 1, ProgressInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	var only string
	if err := pool.QueryRow(ctx, "SELECT work_id FROM work_embeddings").Scan(&only); err != nil || s.Embedded != 1 || only != "W1" {
		t.Errorf("limit should embed the most-read work first: %+v %s %v", s, only, err)
	}
}
