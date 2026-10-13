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

func redirectsFixture(t *testing.T, dir string) string {
	return dumpFile(t, dir, "redirects.txt", []rec{
		{"/type/redirect", "/works/OL99W", map[string]any{"location": "/works/OL1W"}},
		{"/type/redirect", "/works/OL98W", map[string]any{"location": "/works/OL6W"}},
		{"/type/redirect", "/authors/OL9A", map[string]any{"location": "/authors/OL1A"}},
		{"/type/redirect", "/books/OL97M", map[string]any{"location": "/books/OL1M"}},
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
