package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nem0z/book-finder/internal/db"
)

// Set BOOKFINDER_TEST_DATABASE_URL to a disposable database to run this test; it truncates the catalogue.
const testDBEnv = "BOOKFINDER_TEST_DATABASE_URL"

type rec struct {
	typ, key string
	data     map[string]any
}

func dumpFile(t *testing.T, dir, name string, recs []rec, gz bool) string {
	t.Helper()
	var b strings.Builder
	for _, r := range recs {
		data, err := json.Marshal(r.data)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(r.typ + "\t" + r.key + "\t1\t2026-09-30T00:00:00\t" + string(data) + "\n")
	}
	return writeFile(t, dir, name, b.String(), gz)
}

func writeFile(t *testing.T, dir, name, content string, gz bool) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := []byte(content)
	if gz {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		w.Write(body) //nolint:errcheck
		w.Close()     //nolint:errcheck
		body = buf.Bytes()
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func ref(k string) map[string]any { return map[string]any{"key": k} }

func author(k string) map[string]any {
	return map[string]any{"author": ref(k), "type": ref("/type/author_role")}
}

func text(v string) map[string]any { return map[string]any{"type": "/type/text", "value": v} }

func TestRunEndToEnd(t *testing.T) {
	url := os.Getenv(testDBEnv)
	if url == "" {
		t.Skipf("%s not set", testDBEnv)
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
	if applied, err := db.Migrate(ctx, pool); err != nil || len(applied) != 0 {
		t.Fatalf("second migrate should be a no-op: %v %v", applied, err)
	}

	dir := t.TempDir()
	works := dumpFile(t, dir, "works.txt.gz", []rec{
		{"/type/work", "/works/OL1W", map[string]any{"key": "/works/OL1W", "title": "The Thirst",
			"authors": []any{author("/authors/OL1A")}, "subjects": []any{"Fiction", "Detective and mystery stories"},
			"description": text("Harry Hole hunts a killer in Oslo."), "first_publish_date": "2017", "covers": []any{123}}},
		{"/type/work", "/works/OL2W", map[string]any{"key": "/works/OL2W", "title": "Tørst",
			"authors": []any{author("/authors/OL1A")}, "subjects": []any{"Norwegian fiction"}}},
		{"/type/work", "/works/OL3W", map[string]any{"key": "/works/OL3W", "title": "Cookbook",
			"authors": []any{author("/authors/OL2A")}, "subjects": []any{"Cooking"}}},
		{"/type/work", "/works/OL4W", map[string]any{"key": "/works/OL4W", "title": "Roman",
			"authors": []any{author("/authors/OL3A")}, "subjects": []any{"French fiction"}}},
		{"/type/work", "/works/OL5W", map[string]any{"key": "/works/OL5W", "title": "Facts",
			"subjects": []any{"Juvenile nonfiction"}}},
		{"/type/work", "/works/OL6W", map[string]any{"key": "/works/OL6W", "title": "Old Author Key",
			"authors": []any{author("/authors/OL9A")}, "subjects": []any{"Fiction"}}},
		{"/type/work", "/works/OL7W", map[string]any{"key": "/works/OL7W", "title": "Broken",
			"subjects": "Fiction", "authors": 5}},
	}, true)
	editions := dumpFile(t, dir, "editions.txt", []rec{
		{"/type/edition", "/books/OL1M", map[string]any{"key": "/books/OL1M", "title": "The Thirst",
			"works": []any{ref("/works/OL1W")}, "languages": []any{ref("/languages/eng")}, "number_of_pages": 300,
			"isbn_13": []any{"978-0-00-000000-1"}, "publish_date": "2017", "physical_format": "Paperback",
			"genres": []any{"Fiction."}}},
		{"/type/edition", "/books/OL2M", map[string]any{"key": "/books/OL2M", "title": "The Thirst",
			"works": []any{ref("/works/OL1W")}, "languages": []any{ref("/languages/eng")}, "number_of_pages": 320}},
		{"/type/edition", "/books/OL3M", map[string]any{"key": "/books/OL3M", "title": "Durst",
			"works": []any{ref("/works/OL1W")}, "languages": []any{ref("/languages/ger")}}},
		{"/type/edition", "/books/OL4M", map[string]any{"key": "/books/OL4M", "title": "Tørst",
			"works": []any{ref("/works/OL2W")}, "languages": []any{ref("/languages/nob")}, "publish_date": "2017"}},
		{"/type/edition", "/books/OL5M", map[string]any{"key": "/books/OL5M", "title": "The Thirst",
			"works": []any{ref("/works/OL2W")}, "languages": []any{ref("/languages/eng")},
			"translated_from": []any{ref("/languages/nor")}, "number_of_pages": 400, "publish_date": "2018",
			"description": text("English edition blurb.")}},
		{"/type/edition", "/books/OL6M", map[string]any{"key": "/books/OL6M", "title": "Roman",
			"works": []any{ref("/works/OL4W")}, "languages": []any{ref("/languages/fre")}}},
		{"/type/edition", "/books/OL7M", map[string]any{"key": "/books/OL7M", "title": "Old Author Key",
			"works": []any{ref("/works/OL6W")}, "languages": []any{ref("/languages/eng")}}},
		{"/type/edition", "/books/OL8M", map[string]any{"key": "/books/OL8M", "title": "Cookbook",
			"works": []any{ref("/works/OL3W")}, "languages": []any{ref("/languages/eng")}}},
		{"/type/edition", "/books/OL9M", map[string]any{"key": "/books/OL9M", "title": "Merged",
			"works": []any{ref("/works/OL98W")}, "languages": []any{ref("/languages/eng")}}},
	}, false)
	authors := dumpFile(t, dir, "authors.txt.gz", []rec{
		{"/type/author", "/authors/OL1A", map[string]any{"key": "/authors/OL1A", "name": "Jo Nesbø",
			"remote_ids": map[string]any{"wikidata": "Q123"}}},
		{"/type/author", "/authors/OL2A", map[string]any{"key": "/authors/OL2A", "name": "Chef"}},
		{"/type/author", "/authors/OL3A", map[string]any{"key": "/authors/OL3A", "name": "Auteur"}},
	}, true)
	redirects := dumpFile(t, dir, "redirects.txt", []rec{
		{"/type/redirect", "/works/OL99W", map[string]any{"location": "/works/OL1W"}},
		{"/type/redirect", "/works/OL98W", map[string]any{"location": "/works/OL6W"}},
		{"/type/redirect", "/authors/OL9A", map[string]any{"location": "/authors/OL1A"}},
	}, false)
	ratings := writeFile(t, dir, "ratings.txt", "/works/OL1W\t\t5\t2020-01-01\n/works/OL99W\t/books/OL1M\t3\t2021-01-01\n/works/OL3W\t\t1\t2021-01-01\n", false)
	readingLog := writeFile(t, dir, "reading-log.txt", "/works/OL1W\t\tWant to Read\t2020-01-01\n/works/OL1W\t\tAlready Read\t2020-01-01\n/works/OL6W\t\tCurrently Reading\t2020-01-01\n", false)

	stats, err := Run(ctx, pool, Options{Sources: Sources{
		Works: works, Editions: editions, Authors: authors,
		Ratings: ratings, ReadingLog: readingLog, Redirects: redirects,
	}, Workers: 3, ProgressInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	if stats.CandidateWorks != 4 || stats.Works != 3 || stats.Editions != 5 || stats.Authors != 1 ||
		stats.WorkAuthors != 3 || stats.WorksWithDescription != 2 || stats.DecodeErrors != 1 {
		t.Errorf("stats: %+v", stats)
	}

	type work struct {
		desc, source, lang     *string
		year, pages            *int16
		genres, authors        []string
		editions, english      int32
		ratingAvg              *float32
		ratingCount, want, cur int32
		read                   int32
		cover                  *int64
	}
	get := func(id string) work {
		var w work
		err := pool.QueryRow(ctx, `SELECT description, description_source, original_language, first_publish_year,
			page_count, genres, author_names, edition_count, english_edition_count, rating_avg, rating_count,
			want_to_read, currently_reading, already_read, cover_id FROM works WHERE id = $1`, id).Scan(
			&w.desc, &w.source, &w.lang, &w.year, &w.pages, &w.genres, &w.authors, &w.editions, &w.english,
			&w.ratingAvg, &w.ratingCount, &w.want, &w.cur, &w.read, &w.cover)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		return w
	}

	w1 := get("OL1W")
	if *w1.source != "work" || *w1.lang != "eng" || *w1.year != 2017 || *w1.pages != 310 ||
		!slices.Equal(w1.genres, []string{"Fiction"}) || !slices.Equal(w1.authors, []string{"Jo Nesbø"}) ||
		w1.editions != 3 || w1.english != 2 || *w1.ratingAvg != 4 || w1.ratingCount != 2 ||
		w1.want != 1 || w1.read != 1 || *w1.cover != 123 {
		t.Errorf("OL1W: %+v", w1)
	}
	w2 := get("OL2W")
	if *w2.desc != "English edition blurb." || *w2.source != "edition" || *w2.lang != "nor" ||
		*w2.year != 2017 || *w2.pages != 400 || w2.editions != 2 || w2.english != 1 {
		t.Errorf("OL2W: %+v", w2)
	}
	w6 := get("OL6W")
	if !slices.Equal(w6.authors, []string{"Jo Nesbø"}) || w6.english != 2 || w6.cur != 1 || w6.desc != nil {
		t.Errorf("OL6W: %+v", w6)
	}

	var isbns []string
	if err := pool.QueryRow(ctx, "SELECT isbn_13 FROM editions WHERE id = 'OL1M'").Scan(&isbns); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(isbns, []string{"9780000000001"}) {
		t.Errorf("isbn: %v", isbns)
	}

	var hits []string
	rows, err := pool.Query(ctx, "SELECT id FROM works WHERE search_tsv @@ plainto_tsquery('english', 'killer oslo') ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		rows.Scan(&id) //nolint:errcheck
		hits = append(hits, id)
	}
	if !slices.Equal(hits, []string{"OL1W"}) {
		t.Errorf("full-text hits: %v", hits)
	}

	var indexes int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_indexes WHERE indexname = ANY($1)", db.IndexNames()).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != len(db.IndexNames()) {
		t.Errorf("indexes rebuilt: %d of %d", indexes, len(db.IndexNames()))
	}

	// A second run must fully replace the catalogue rather than append to it.
	again, err := Run(ctx, pool, Options{Sources: Sources{Works: works, Editions: editions, Authors: authors, Redirects: redirects}, ProgressInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	if again.Works != 3 || again.Editions != 5 {
		t.Errorf("rerun stats: %+v", again)
	}
}
