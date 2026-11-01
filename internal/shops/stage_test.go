package shops

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/db"
)

// Set BOOKFINDER_TEST_DATABASE_URL to a disposable database to run the database tests; they empty works.
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

	if _, err := pool.Exec(ctx, `TRUNCATE works, work_authors, authors;
		INSERT INTO works (id, title) VALUES ('OL1W', 'Persians');
		INSERT INTO works (id, source, title) VALUES ('chapters:9780141439518', 'chapters', 'Gone')`); err != nil {
		t.Fatal(err)
	}

	return pool
}

// workRows lists the works as "id source title year description_source cover_url".
func workRows(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT id, source, title, first_publish_year, description_source, cover_url
		FROM works ORDER BY id COLLATE "C"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id, source, title string
		var year *int16
		var descSource, cover *string
		if err := rows.Scan(&id, &source, &title, &year, &descSource, &cover); err != nil {
			t.Fatal(err)
		}

		out = append(out, fmt.Sprint(id, " ", source, " ", title, " ", deref(year), " ", deref(descSource), " ", deref(cover)))
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

func deref[T any](p *T) any {
	if p == nil {
		return "-"
	}

	return *p
}

func TestStageReplace(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	stage, err := NewStage(ctx, pool, SourceChapters)
	if err != nil {
		t.Fatal(err)
	}

	crawdads := Book{Source: SourceChapters, ISBN13: "9781472277299", Title: "Where the Crawdads Sing",
		Authors: []string{"Delia Owens"}, Year: 2019, Genres: []string{"Fiction"}, Description: "A marsh girl.",
		URL: "https://shop/products/a", CoverURL: "https://cdn/a.jpg"}
	verity := Book{Source: SourceChapters, ISBN13: "9780008742775", Title: "Verity", URL: "https://shop/products/b"}
	again := crawdads
	again.Title = "Again"
	for _, page := range [][]Book{{crawdads, verity}, {again}, nil} {
		if err := stage.Add(ctx, page); err != nil {
			t.Fatal(err)
		}
	}

	if err := stage.Add(ctx, []Book{{Source: "other", ISBN13: "9780306406157"}}); err == nil {
		t.Error("a book of another source was staged")
	}

	r, err := stage.Replace(ctx)
	if err != nil || r != (Replaced{Deleted: 1, Inserted: 2}) {
		t.Fatalf("replace: %+v, %v", r, err)
	}

	want := []string{
		"OL1W openlibrary Persians - - -",
		"chapters:9780008742775 chapters Verity - - -",
		"chapters:9781472277299 chapters Where the Crawdads Sing 2019 edition https://cdn/a.jpg",
	}
	if got := workRows(t, pool); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("works:\n%q\nwant\n%q", got, want)
	}

	var matches []string
	for _, q := range []string{"crawdads", "delia", "fiction", "marsh"} {
		var id string
		err := pool.QueryRow(ctx, `SELECT id FROM works WHERE search_tsv @@ plainto_tsquery('english', $1)`, q).Scan(&id)
		matches = append(matches, fmt.Sprint(q, "=", id, err))
	}

	if fmt.Sprint(matches) != "[crawdads=chapters:9781472277299<nil> delia=chapters:9781472277299<nil> "+
		"fiction=chapters:9781472277299<nil> marsh=chapters:9781472277299<nil>]" {
		t.Errorf("search_tsv: %v", matches)
	}

	var stage2 *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, stageTable).Scan(&stage2); err != nil || stage2 != nil {
		t.Errorf("stage left behind: %v, %v", deref(stage2), err)
	}
}

func TestStageDrop(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	before := workRows(t, pool)
	stage, err := NewStage(ctx, pool, SourceChapters)
	if err != nil {
		t.Fatal(err)
	}

	if err := stage.Add(ctx, []Book{{Source: SourceChapters, ISBN13: "9780008742775", Title: "Verity"}}); err != nil {
		t.Fatal(err)
	}

	if err := stage.Drop(ctx); err != nil {
		t.Fatal(err)
	}

	if err := stage.Drop(ctx); err != nil {
		t.Errorf("drop again: %v", err)
	}

	if _, err := stage.Replace(ctx); err == nil {
		t.Error("replaced from a dropped stage")
	}

	if got := workRows(t, pool); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Errorf("works changed: %q, want %q", got, before)
	}
}
