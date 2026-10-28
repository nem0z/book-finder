package enrich

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

	if _, err := pool.Exec(ctx, "TRUNCATE works, work_authors, authors, work_embeddings, work_keywords"); err != nil {
		t.Fatal(err)
	}

	return pool
}

func ids(books []Book) []string {
	var out []string
	for _, b := range books {
		out = append(out, b.ID)
	}

	return out
}

func TestStore(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO works (id, title, author_names, description, rating_count) VALUES
		('OL1W', 'Most rated', '{Ann}', 'd1', 30), ('OL2W', 'Second', '{}', 'd2', 20),
		('OL3W', 'Third', '{}', 'd3', 20), ('OL4W', 'No vector', '{}', 'd4', 50)`); err != nil {
		t.Fatal(err)
	}

	zero := "[" + strings.Repeat("0,", 767) + "1]"
	if _, err := pool.Exec(ctx, `INSERT INTO work_embeddings (work_id, model, content_hash, embedding)
		SELECT id, 'e', 'h', $1::halfvec FROM unnest('{OL1W,OL2W,OL3W}'::text[]) AS id`, zero); err != nil {
		t.Fatal(err)
	}

	p, err := ReadPending(ctx, pool, "m", nil, Cursor{}, 2)
	if err != nil || !slices.Equal(ids(p.Books), []string{"OL1W", "OL2W"}) || p.Read != 2 {
		t.Fatalf("first page: %+v, %v", p, err)
	}

	if p, err = ReadPending(ctx, pool, "m", nil, p.Next, 2); err != nil || !slices.Equal(ids(p.Books), []string{"OL3W"}) || p.Read != 1 {
		t.Fatalf("second page: %+v, %v", p, err)
	}

	first, _ := ReadPending(ctx, pool, "m", nil, Cursor{}, 1)
	b := first.Books[0]
	k := Keywords{Keywords: []string{"Orcs", "coffee shops"}, Places: []string{"Thune"}, EraFrom: 0, EraTo: 1200, EraLabel: "invented world"}
	if err := Save(ctx, pool, "m", []Extracted{{WorkID: b.ID, Hash: b.Hash, Keywords: k}}); err != nil {
		t.Fatal(err)
	}

	var kws []string
	var eraFrom, eraTo *int16
	var matches bool
	if err := pool.QueryRow(ctx, `SELECT keywords, era_from, era_to, keywords_tsv @@ to_tsquery('english', 'orc & shop & thune')
		FROM work_keywords WHERE work_id = 'OL1W'`).Scan(&kws, &eraFrom, &eraTo, &matches); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(kws, []string{"orcs", "coffee shops"}) || eraFrom != nil || eraTo == nil || *eraTo != 1200 || !matches {
		t.Errorf("stored: %q, %v-%v, matches %v", kws, eraFrom, eraTo, matches)
	}

	if p, _ := ReadPending(ctx, pool, "m", nil, Cursor{}, 10); !slices.Equal(ids(p.Books), []string{"OL2W", "OL3W"}) || p.Read != 3 {
		t.Errorf("after save: %+v", p)
	}

	if p, _ := ReadPending(ctx, pool, "other model", []string{"OL1W", "OL3W"}, Cursor{}, 10); !slices.Equal(ids(p.Books), []string{"OL1W", "OL3W"}) {
		t.Errorf("other model, by id: %+v", p)
	}

	if _, err := pool.Exec(ctx, "DELETE FROM works WHERE id = 'OL1W'"); err != nil {
		t.Fatal(err)
	}

	if n, err := Prune(ctx, pool); err != nil || n != 1 {
		t.Errorf("prune: %d, %v", n, err)
	}

	if _, err := pool.Exec(ctx, "DELETE FROM works"); err != nil {
		t.Fatal(err)
	}

	if _, err := Prune(ctx, pool); !errors.Is(err, ErrEmptyCatalogue) {
		t.Errorf("prune on an empty catalogue: %v", err)
	}
}
