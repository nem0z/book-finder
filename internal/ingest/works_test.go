package ingest

import (
	"context"
	"maps"
	"slices"
	"testing"
)

// loadThrough runs the given loader steps in order.
func loadThrough(t *testing.T, l *loader, steps ...func(context.Context) error) {
	t.Helper()
	for _, step := range steps {
		if err := step(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadWorks(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dir := t.TempDir()
	l := newLoader(pool, Options{Sources: Sources{Works: worksFixture(t, dir), Redirects: redirectsFixture(t, dir)},
		Workers: 3, ProgressInterval: -1})
	loadThrough(t, l, l.prepare, l.loadRedirects, l.loadWorks)

	if got := slices.Sorted(maps.Keys(l.works)); !slices.Equal(got, []string{"OL1W", "OL2W", "OL4W", "OL6W"}) {
		t.Errorf("candidates: %v", got)
	}

	if !l.works["OL1W"].hasDescription || l.works["OL2W"].hasDescription {
		t.Error("hasDescription")
	}

	if got := slices.Sorted(maps.Keys(l.authorsNeeded)); !slices.Equal(got, []string{"OL1A", "OL3A"}) {
		t.Errorf("authors needed (OL9A redirects to OL1A): %v", got)
	}

	if l.stats.CandidateWorks != 4 || l.stats.DecodeErrors != 1 {
		t.Errorf("stats: %+v", l.stats)
	}

	var title, desc string
	var year int16
	var subjects, authors []string
	if err := pool.QueryRow(ctx, `SELECT title, description, first_publish_year, subjects, author_ids
		FROM stage_works WHERE id = 'OL1W'`).Scan(&title, &desc, &year, &subjects, &authors); err != nil {
		t.Fatal(err)
	}

	if title != "The Thirst" || desc != "Harry Hole hunts a killer in Oslo." || year != 2017 ||
		len(subjects) != 2 || !slices.Equal(authors, []string{"OL1A"}) {
		t.Errorf("OL1W staged as %q %q %d %v %v", title, desc, year, subjects, authors)
	}

	var staged int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM stage_works").Scan(&staged); err != nil || staged != 4 {
		t.Errorf("staged works: %d %v", staged, err)
	}
}
