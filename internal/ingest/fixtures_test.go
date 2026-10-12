package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
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
