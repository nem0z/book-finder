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
