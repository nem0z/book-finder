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

	if err := l.prepare(ctx); err != nil {
		t.Fatal(err)
	}

	var indexes int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_indexes WHERE indexname = ANY($1)", db.IndexNames()).Scan(&indexes); err != nil {
		t.Fatal(err)
	}

	if indexes != 0 {
		t.Errorf("secondary indexes should be dropped before loading, %d left", indexes)
	}
}
