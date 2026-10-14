package ingest

import (
	"context"
	"slices"
	"testing"

	"github.com/nem0z/book-finder/internal/db"
)

// TestRun loads every fixture. Of the candidates, OL1W (own description) and OL4W (description from
// its English edition) are kept; OL2W has no English edition and OL6W no description.
func TestRun(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dir := t.TempDir()
	stats, err := Run(ctx, pool, Options{Sources: Sources{
		Works: worksFixture(t, dir), Editions: editionsFixture(t, dir), Authors: authorsFixture(t, dir),
		Redirects: redirectsFixture(t, dir),
	}, Workers: 3, ProgressInterval: -1})
	if err != nil {
		t.Fatal(err)
	}

	if stats.CandidateWorks != 4 || stats.Authors != 2 {
		t.Errorf("stats: %+v", stats)
	}

	type work struct {
		id, title, desc, source, lang string
		year, pages                   int16
		cover                         int64
		genres, authors               []string
	}

	rows, err := pool.Query(ctx, `SELECT id, title, description, description_source, coalesce(original_language, ''),
		coalesce(first_publish_year, 0), coalesce(page_count, 0), coalesce(cover_id, 0), genres, author_names
		FROM works ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}

	var works []work
	for rows.Next() {
		var w work
		if err := rows.Scan(&w.id, &w.title, &w.desc, &w.source, &w.lang, &w.year, &w.pages, &w.cover, &w.genres, &w.authors); err != nil {
			t.Fatal(err)
		}

		works = append(works, w)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(works) != 2 || works[0].id != "OL1W" || works[1].id != "OL4W" {
		t.Fatalf("works: %+v", works)
	}

	w := works[0]
	if w.title != "The Thirst" || w.source != "work" || w.year != 2017 || w.lang != "nor" || w.pages != 480 ||
		w.cover != 123 || len(w.genres) != 2 || !slices.Equal(w.authors, []string{"Jo Nesbø"}) {
		t.Errorf("OL1W: %+v", w)
	}

	w = works[1]
	if w.desc != "A French novel, translated." || w.source != "edition" || w.year != 1950 || w.lang != "fre" ||
		!slices.Equal(w.authors, []string{"Émile Zola"}) {
		t.Errorf("OL4W: %+v", w)
	}

	var links []string
	rows, err = pool.Query(ctx, "SELECT work_id || ':' || author_id || ':' || position FROM work_authors ORDER BY 1")
	if err != nil {
		t.Fatal(err)
	}

	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}

		links = append(links, s)
	}

	if !slices.Equal(links, []string{"OL1W:OL1A:0", "OL4W:OL3A:0"}) {
		t.Errorf("work_authors: %v", links)
	}

	var authors, staging, indexes int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM authors),
		(SELECT count(*) FROM pg_tables WHERE tablename LIKE 'stage_%'),
		(SELECT count(*) FROM pg_indexes WHERE indexname = ANY($1))`, db.IndexNames()).Scan(&authors, &staging, &indexes); err != nil {
		t.Fatal(err)
	}

	if authors != 2 || staging != 0 || indexes != len(db.IndexNames()) {
		t.Errorf("authors %d, staging tables %d, indexes %d of %d", authors, staging, indexes, len(db.IndexNames()))
	}

	var hits int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM works WHERE search_tsv @@ plainto_tsquery('english', 'Oslo killer')").
		Scan(&hits); err != nil || hits != 1 {
		t.Errorf("full-text search: %d %v", hits, err)
	}
}
