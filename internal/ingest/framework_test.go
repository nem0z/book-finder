package ingest

import (
	"context"
	"testing"

	"github.com/nem0z/book-finder/internal/db"
)

func TestRunRequiresMainDumps(t *testing.T) {
	if _, err := Run(context.Background(), nil, Options{Sources: Sources{Works: "w"}}); err == nil {
		t.Error("expected an error when editions and authors are missing")
	}
}

func TestPrepare(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	l := newLoader(pool, Options{Sources: Sources{Redirects: redirectsFixture(t, t.TempDir())}, ProgressInterval: -1})

	if _, err := pool.Exec(ctx, `TRUNCATE works, work_authors, authors;
		INSERT INTO works (id, title) VALUES ('OL1W', 'Open Library');
		INSERT INTO works (id, title, source) VALUES ('chapters:9780000000001', 'Bookshop', 'chapters')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "TRUNCATE works, work_authors;"+db.IndexesSQL()) }) //nolint:errcheck // best effort

	if err := l.prepare(ctx); err != nil {
		t.Fatal(err)
	}

	var ids []string
	if err := pool.QueryRow(ctx, "SELECT coalesce(array_agg(id), '{}') FROM works").Scan(&ids); err != nil {
		t.Fatal(err)
	}

	if len(ids) != 1 || ids[0] != "chapters:9780000000001" {
		t.Errorf("only bookshop works should survive, got %v", ids)
	}

	var indexes int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_indexes WHERE indexname = ANY($1)", db.IndexNames()).Scan(&indexes); err != nil {
		t.Fatal(err)
	}

	if indexes != 0 {
		t.Errorf("secondary indexes should be dropped before loading, %d left", indexes)
	}
}
