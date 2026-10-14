package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/db"
)

// Set BOOKFINDER_TEST_DATABASE_URL to a disposable database to run the database tests;
// they truncate the catalogue.
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

	return pool
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

// rec is one dump line: type, key and the JSON record.
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

func ref(k string) map[string]any { return map[string]any{"key": k} }

// redirectsFixture: 4 redirects kept (one target without its path), 5 skipped.
func redirectsFixture(t *testing.T, dir string) string {
	return dumpFile(t, dir, "redirects.txt", []rec{
		{"/type/redirect", "/works/OL99W", map[string]any{"location": "/works/OL1W"}},
		{"/type/redirect", "/works/OL98W", map[string]any{"location": "/works/OL6W"}},
		{"/type/redirect", "/authors/OL9A", map[string]any{"location": "OL1A"}},           // path-less target
		{"/type/redirect", "/books/OL97M", map[string]any{"location": "/books/OL1M"}},     // edition
		{"/type/redirect", "/works/OL96W", map[string]any{"location": "/books/OL2M"}},     // other kind
		{"/type/redirect", "/authors/OL95W", map[string]any{"location": "/authors/OL1A"}}, // letter mismatch
		{"/type/redirect", "/books/ia:cihm_1", map[string]any{"location": "/books/OL3M"}}, // odd key
		{"/type/redirect", "/about/team", map[string]any{"location": "/about/people"}},    // odd key
		{"/type/redirect", "/works/OL94W", map[string]any{}},                              // no target
	}, false)
}

func author(k string) map[string]any {
	return map[string]any{"author": ref(k), "type": ref("/type/author_role")}
}

func text(v string) map[string]any { return map[string]any{"type": "/type/text", "value": v} }

// worksFixture: OL1W, OL2W, OL4W, OL6W are fiction candidates; OL3W is about fiction, OL5W is
// nonfiction, OL7W is malformed.
func worksFixture(t *testing.T, dir string) string {
	return dumpFile(t, dir, "works.txt.gz", []rec{
		{"/type/work", "/works/OL1W", map[string]any{"key": "/works/OL1W", "title": "The Thirst",
			"authors": []any{author("/authors/OL1A")}, "subjects": []any{"Fiction", "Detective and mystery stories"},
			"description": text("Harry Hole hunts a killer in Oslo."), "first_publish_date": "2017", "covers": []any{123}}},
		{"/type/work", "/works/OL2W", map[string]any{"key": "/works/OL2W", "title": "Tørst",
			"authors": []any{author("/authors/OL1A")}, "subjects": []any{"Norwegian fiction"}}},
		{"/type/work", "/works/OL3W", map[string]any{"key": "/works/OL3W", "title": "Cookbook",
			"authors": []any{author("/authors/OL2A")}, "subjects": []any{"Cooking", "Fiction, History and criticism"}}},
		{"/type/work", "/works/OL4W", map[string]any{"key": "/works/OL4W", "title": "Roman",
			"authors": []any{author("/authors/OL3A")}, "subjects": []any{"Fiction", "French fiction"}}},
		{"/type/work", "/works/OL5W", map[string]any{"key": "/works/OL5W", "title": "Facts",
			"subjects": []any{"Juvenile nonfiction"}}},
		{"/type/work", "/works/OL6W", map[string]any{"key": "/works/OL6W", "title": "Old Author Key",
			"authors": []any{author("/authors/OL9A")}, "subjects": []any{"Fiction"}}},
		{"/type/work", "/works/OL7W", map[string]any{"key": "/works/OL7W", "title": "Broken",
			"subjects": "Fiction", "authors": 5}},
	}, true)
}

// editionsFixture, for the candidates of worksFixture (OL1W, OL2W, OL4W, OL6W; OL1W has a description):
// OL1W has two English editions, one through a redirected work key; OL2W only a Norwegian one; OL4W a
// French edition then an English one with a description; OL6W an English one. OL6M is an edition of a
// work that is not a candidate, OL7M has no work and OL9M has an author key where a work is expected.
func editionsFixture(t *testing.T, dir string) string {
	eng := []any{ref("/languages/eng")}
	return dumpFile(t, dir, "editions.txt.gz", []rec{
		{"/type/edition", "/books/OL1M", map[string]any{"key": "/books/OL1M", "works": []any{ref("/works/OL1W")},
			"languages": eng, "translated_from": []any{ref("/languages/nor")}, "publish_date": "June 13, 2018",
			"number_of_pages": 480, "isbn_13": []any{"978-0-00-000000-1"}, "genres": []any{"Fiction."}, "covers": []any{7}}},
		{"/type/edition", "/books/OL2M", map[string]any{"key": "/books/OL2M", "works": []any{ref("/works/OL99W")},
			"languages": eng, "publish_date": "2019", "number_of_pages": "512",
			"description": text("An English edition description."), "genres": []any{"Thriller", "fiction"}}},
		{"/type/edition", "/books/OL3M", map[string]any{"key": "/books/OL3M", "works": []any{ref("/works/OL2W")},
			"languages": []any{ref("/languages/nor")}, "publish_date": "2017"}},
		{"/type/edition", "/books/OL4M", map[string]any{"key": "/books/OL4M", "works": []any{ref("/works/OL4W")},
			"languages": []any{ref("/languages/fre")}, "publish_date": "1950"}},
		{"/type/edition", "/books/OL5M", map[string]any{"key": "/books/OL5M", "works": []any{ref("/works/OL4W")},
			"languages": eng, "publish_date": "c1990", "description": "A French novel, translated."}},
		{"/type/edition", "/books/OL6M", map[string]any{"key": "/books/OL6M", "works": []any{ref("/works/OL3W")},
			"languages": eng}},
		{"/type/edition", "/books/OL7M", map[string]any{"key": "/books/OL7M", "languages": eng}},
		{"/type/edition", "/books/OL8M", map[string]any{"key": "/books/OL8M", "works": []any{ref("/works/OL6W")},
			"languages": eng, "number_of_pages": 3}},
		{"/type/edition", "/books/OL9M", map[string]any{"key": "/books/OL9M", "works": []any{ref("/authors/OL1A")},
			"languages": eng}},
	}, true)
}

// authorsFixture: OL1A and OL3A are referenced by worksFixture's candidates. OL2A is not, OL4A has
// no name and OL5A has broken JSON.
func authorsFixture(t *testing.T, dir string) string {
	return dumpFile(t, dir, "authors.txt.gz", []rec{
		{"/type/author", "/authors/OL1A", map[string]any{"key": "/authors/OL1A", "name": "Jo Nesbø ",
			"birth_date": "29 March 1960", "remote_ids": map[string]any{"wikidata": "Q123"}}},
		{"/type/author", "/authors/OL2A", map[string]any{"key": "/authors/OL2A", "name": "Cook"}},
		{"/type/author", "/authors/OL3A", map[string]any{"key": "/authors/OL3A", "name": "Émile Zola",
			"death_date": "1902"}},
		{"/type/author", "/authors/OL4A", map[string]any{"key": "/authors/OL4A", "name": " "}},
		{"/type/author", "/authors/OL5A", map[string]any{"key": "/authors/OL5A", "name": []any{1}}},
	}, true)
}

// ratingsFixture: OL1W gets 5 and 4 (one through the redirected OL99W), OL4W gets 3. The OL3W rating
// (not a candidate) and the malformed lines are skipped.
func ratingsFixture(t *testing.T, dir string) string {
	return writeFile(t, dir, "ratings.txt.gz", strings.Join([]string{
		"/works/OL1W\t/books/OL1M\t5\t2018-06-20",
		"/works/OL99W\t\\N\t4\t2019-01-01",
		"/works/OL4W\t\\N\t3\t2020-01-01",
		"/works/OL3W\t\\N\t5\t2020-01-01",
		"/works/OL1W\t\\N\t9\t2020-01-01",
		"/works/OL1W\t\\N\tx\t2020-01-01",
		"/works/OL1W\t\\N",
	}, "\n")+"\n", true)
}
