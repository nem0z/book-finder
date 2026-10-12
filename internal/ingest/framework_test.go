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

func TestResolve(t *testing.T) {
	l := newLoader(nil, Options{})
	l.redirects = map[string]string{"/works/A": "/works/B", "/works/B": "/works/C", "/works/L": "/works/L"}
	for in, want := range map[string]string{"/works/A": "/works/C", "/works/C": "/works/C", "/works/L": "/works/L", "/works/X": "/works/X"} {
		if got := l.resolve(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

func TestPrepareAndRedirects(t *testing.T) {
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

	if err := l.loadRedirects(ctx); err != nil {
		t.Fatal(err)
	}

	if l.stats.Redirects != 3 { // the /books/ redirect is ignored
		t.Errorf("redirects: %d", l.stats.Redirects)
	}

	if got := l.resolve("/works/OL99W"); got != "/works/OL1W" {
		t.Errorf("resolve: %s", got)
	}
}
