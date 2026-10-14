package ingest

import (
	"context"
	"testing"
)

func TestLoadAuthors(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	l := newLoader(pool, Options{Sources: Sources{Authors: authorsFixture(t, t.TempDir())}, Workers: 3, ProgressInterval: -1})
	l.authorsNeeded = map[string]struct{}{"OL1A": {}, "OL3A": {}, "OL4A": {}, "OL5A": {}}
	loadThrough(t, l, l.prepare, l.loadAuthors)

	if l.stats.Authors != 2 || l.stats.DecodeErrors != 1 {
		t.Errorf("stats: %+v", l.stats)
	}

	var name, birth, wikidata string
	if err := pool.QueryRow(ctx, "SELECT name, birth_date, wikidata_id FROM authors WHERE id = 'OL1A'").
		Scan(&name, &birth, &wikidata); err != nil {
		t.Fatal(err)
	}

	if name != "Jo Nesbø" || birth != "29 March 1960" || wikidata != "Q123" {
		t.Errorf("OL1A: %q %q %q", name, birth, wikidata)
	}

	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM authors WHERE id = 'OL3A' AND wikidata_id IS NULL AND death_date = '1902'").
		Scan(&n); err != nil || n != 1 {
		t.Errorf("OL3A: %d %v", n, err)
	}
}

func TestNeededAuthor(t *testing.T) {
	l := newLoader(nil, Options{})
	l.authorsNeeded = map[string]struct{}{"OL1A": {}}
	for line, want := range map[string]bool{
		"/type/author\t/authors/OL1A\t1\t2026\t{}": true,
		"/type/author\t/authors/OL2A\t1\t2026\t{}": false,
		"/type/author\t/authors/OL1A":              false, // no tab after the key
	} {
		if got := l.neededAuthor([]byte(line)); got != want {
			t.Errorf("%q: got %v", line, got)
		}
	}
}
