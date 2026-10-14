package embed

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/db"
)

// Set BOOKFINDER_TEST_DATABASE_URL to a disposable database to run the database tests; they empty
// the catalogue.
const testDBEnv = "BOOKFINDER_TEST_DATABASE_URL"

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

	if _, err := pool.Exec(ctx, "TRUNCATE works, work_authors, authors, work_embeddings"); err != nil {
		t.Fatal(err)
	}

	return pool
}

// fakeEmbedder returns a unit vector and records what it embedded.
type fakeEmbedder struct{ titles []string }

func (f *fakeEmbedder) Model() string { return "fake" }

func (f *fakeEmbedder) EmbedDocuments(_ context.Context, docs []Doc) ([][]float32, error) {
	vecs := make([][]float32, len(docs))
	for i, d := range docs {
		f.titles = append(f.titles, d.Title)
		vecs[i] = make([]float32, Dims)
		vecs[i][0] = 1
	}

	return vecs, nil
}

func TestRun(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	long := strings.Repeat("A long and clean summary of the story. ", 10)
	if _, err := pool.Exec(ctx, `INSERT INTO works (id, title, description, rating_count) VALUES
		('OL1W', 'Popular', $1, 10),
		('OL2W', 'Linked', $1 || ' https://example.com', 20),
		('OL3W', 'Short', 'Too short.', 30),
		('OL4W', 'Unrated', $1, 0),
		('OL5W', 'Html', $1 || '<a href="/works/OL1W">', 5)`, long); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO work_embeddings (work_id, model, content_hash, embedding)
		VALUES ('OL9W', 'fake', 'x', array_fill(0.1, ARRAY[768])::halfvec)`); err != nil {
		t.Fatal(err)
	}

	emb := &fakeEmbedder{}
	opts := Options{BatchSize: 1, ProgressInterval: -1}

	opts.Limit = 1
	s, err := Run(ctx, pool, emb, opts)
	if err != nil || s.Embedded != 1 || s.Pruned != 1 || !slices.Equal(emb.titles, []string{"Popular"}) {
		t.Fatalf("limited run: %+v, %v, embedded %v", s, err, emb.titles)
	}

	opts.Limit = 0
	emb.titles = nil
	if s, err := Run(ctx, pool, emb, opts); err != nil || s.Considered != 2 || s.Embedded != 1 || s.UpToDate != 1 ||
		!slices.Equal(emb.titles, []string{"Unrated"}) {
		t.Fatalf("full run: %+v, %v, embedded %v", s, err, emb.titles)
	}

	if _, err := pool.Exec(ctx, "UPDATE works SET description = description || ' More.' WHERE id = 'OL1W'"); err != nil {
		t.Fatal(err)
	}

	emb.titles = nil
	if s, err := Run(ctx, pool, emb, opts); err != nil || s.Embedded != 1 || !slices.Equal(emb.titles, []string{"Popular"}) {
		t.Fatalf("changed text: %+v, %v, embedded %v", s, err, emb.titles)
	}

	var ids []string
	rows, err := pool.Query(ctx, "SELECT work_id FROM work_embeddings ORDER BY 1")
	if err != nil {
		t.Fatal(err)
	}

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}

		ids = append(ids, id)
	}

	if !slices.Equal(ids, []string{"OL1W", "OL4W"}) {
		t.Errorf("stored vectors: %v", ids)
	}
}

func TestRunEmptyCatalogue(t *testing.T) {
	pool := testPool(t)
	if _, err := Run(context.Background(), pool, &fakeEmbedder{}, Options{}); !errors.Is(err, ErrEmptyCatalogue) {
		t.Errorf("got %v", err)
	}
}

func TestVectorLiteral(t *testing.T) {
	if got := VectorLiteral([]float32{0.5, -1, 0}); got != "[0.5,-1,0]" {
		t.Errorf("got %q", got)
	}
}
