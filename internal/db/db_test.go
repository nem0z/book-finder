package db

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testDatabaseEnv names a disposable database for tests that touch Postgres.
const testDatabaseEnv = "BOOKFINDER_TEST_DATABASE_URL"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(testDatabaseEnv)
	if url == "" {
		t.Skipf("%s not set", testDatabaseEnv)
	}
	pool, err := Connect(context.Background(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestMigrate(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if applied, err := Migrate(ctx, pool); err != nil || len(applied) != 0 {
		t.Fatalf("second run should be a no-op: %v %v", applied, err)
	}

	var versions []string
	if err := pool.QueryRow(ctx, "SELECT array_agg(version ORDER BY version) FROM schema_migrations").Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(versions, "001_extensions") {
		t.Errorf("recorded versions: %v", versions)
	}
}

func TestConnectInvalidURL(t *testing.T) {
	if _, err := Connect(context.Background(), "not a url", nil); err == nil {
		t.Error("expected an error")
	}
}
