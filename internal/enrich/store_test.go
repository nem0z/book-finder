package enrich

import (
	"context"
	"errors"
	"os"
	"slices"
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

	if _, err := pool.Exec(ctx, "TRUNCATE works, work_authors, authors, work_keywords"); err != nil {
		t.Fatal(err)
	}

	return pool
}

func TestStore(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO works (id, title, description) VALUES ('OL1W', 'One', 'd1'), ('OL2W', 'Two', 'd2')`); err != nil {
		t.Fatal(err)
	}

	k := Keywords{Keywords: []string{"Orcs", "coffee shops"}, Places: []string{"Thune"}, EraFrom: 0, EraTo: 1200, EraLabel: "invented world"}
	if err := Save(ctx, pool, "m", []Extracted{{WorkID: "OL1W", Hash: "h1", Keywords: k}, {WorkID: "OL2W", Hash: "h2", Keywords: k}}); err != nil {
		t.Fatal(err)
	}

	// Saved again: replaced.
	if err := Save(ctx, pool, "m2", []Extracted{{WorkID: "OL1W", Hash: "h3", Keywords: k}}); err != nil {
		t.Fatal(err)
	}

	var kws []string
	var eraFrom, eraTo *int16
	var model, hash string
	if err := pool.QueryRow(ctx, `SELECT keywords, era_from, era_to, model, content_hash
		FROM work_keywords WHERE work_id = 'OL1W'`).Scan(&kws, &eraFrom, &eraTo, &model, &hash); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(kws, []string{"orcs", "coffee shops"}) || eraFrom != nil || eraTo == nil || *eraTo != 1200 || model != "m2" || hash != "h3" {
		t.Errorf("stored: %q, %v-%v, %s %s", kws, eraFrom, eraTo, model, hash)
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
