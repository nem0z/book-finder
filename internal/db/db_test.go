package db

import (
	"context"
	"os"
	"slices"
	"strings"
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

	var indexes int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_indexes WHERE indexname = ANY($1)", IndexNames()).Scan(&indexes); err != nil {
		t.Fatal(err)
	}

	if indexes != len(IndexNames()) {
		t.Errorf("indexes created: %d of %d", indexes, len(IndexNames()))
	}
}

func TestIndexNames(t *testing.T) {
	names := IndexNames()
	if len(names) < 10 || !slices.Contains(names, "works_search_tsv_idx") || !slices.Contains(names, "works_popularity_idx") {
		t.Errorf("index names: %v", names)
	}
}

func TestMigrationFilesSorted(t *testing.T) {
	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}

	if len(files) == 0 || !slices.IsSorted(files) {
		t.Errorf("migration files: %v", files)
	}
}

func TestConnectInvalidURL(t *testing.T) {
	if _, err := Connect(context.Background(), "not a url", nil); err == nil {
		t.Error("expected an error")
	}
}

// TestDropDescriptionVectors replays migration 009 on a database where a work with a description vector has no
// keyword vector: it must fail and keep the table, then pass once the keyword vector exists.
func TestDropDescriptionVectors(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	const version = "009_drop_work_embeddings"
	if _, err := pool.Exec(ctx, "DELETE FROM schema_migrations WHERE version = $1", version); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `CREATE TABLE work_embeddings (work_id text PRIMARY KEY);
		INSERT INTO work_embeddings VALUES ('OL1W'), ('OL2W');
		TRUNCATE work_keyword_embeddings;
		INSERT INTO work_keyword_embeddings (work_id, model, content_hash, embedding)
		VALUES ('OL1W', 'm', 'h', array_fill(1, ARRAY[768])::halfvec)`); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		pool.Exec(ctx, "DROP TABLE IF EXISTS work_embeddings; TRUNCATE work_keyword_embeddings") //nolint:errcheck // best effort
		Migrate(ctx, pool)                                                                       //nolint:errcheck // records 009 again
	})

	if _, err := Migrate(ctx, pool); err == nil || !strings.Contains(err.Error(), "1 works with a description vector have no keyword vector") {
		t.Errorf("missing keyword vector: %v", err)
	}

	var table *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('work_embeddings')::text").Scan(&table); err != nil || table == nil {
		t.Errorf("the table should be kept: %v", err)
	}

	if _, err := pool.Exec(ctx, "DELETE FROM work_embeddings WHERE work_id = 'OL2W'"); err != nil {
		t.Fatal(err)
	}

	if applied, err := Migrate(ctx, pool); err != nil || !slices.Equal(applied, []string{version}) {
		t.Errorf("all covered: %v, %v", applied, err)
	}
}
