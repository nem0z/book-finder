package search

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/embed"
)

const testDBEnv = "BOOKFINDER_TEST_DATABASE_URL"

type fakeQuery struct{ model string }

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

	if _, err := pool.Exec(ctx, "TRUNCATE works, work_authors, authors, work_embeddings"); err != nil {
		t.Fatal(err)
	}

	return pool
}

// axis returns a unit vector along dimension i, plus a little of dimension 0 so that every vector is
// somewhat close to the query.
func axis(i int, w float32) []float32 {
	v := make([]float32, embed.Dims)
	v[0] = 1
	v[i] = w
	return v
}

func (f fakeQuery) Model() string { return f.model }

func (f fakeQuery) EmbedQuery(context.Context, string) ([]float32, error) { return axis(1, 1), nil }

func TestSemantic(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO works (id, title, description, first_publish_year, page_count, original_language, genres, subjects, cover_id) VALUES
		('OL1W', 'Closest', 'd', 1950, 200, 'jpn', '{Thriller}', '{Fiction,thriller,Oslo}', 7),
		('OL2W', 'Second', 'd', 2001, 400, 'eng', '{}', '{}', NULL),
		('OL3W', 'Far', 'd', 1960, 150, 'eng', '{}', '{}', NULL),
		('OL4W', 'Other model', 'd', 1950, 200, 'jpn', '{}', '{}', NULL)`); err != nil {
		t.Fatal(err)
	}

	for id, v := range map[string][]float32{"OL1W": axis(1, 1), "OL2W": axis(1, 0.5), "OL3W": axis(2, 1), "OL4W": axis(1, 1)} {
		model := "fake"
		if id == "OL4W" {
			model = "other"
		}

		if _, err := pool.Exec(ctx, "INSERT INTO work_embeddings (work_id, model, content_hash, embedding) VALUES ($1, $2, 'h', $3::halfvec)",
			id, model, embed.VectorLiteral(v)); err != nil {
			t.Fatal(err)
		}
	}

	ids := func(f Filters, limit int) []string {
		t.Helper()
		res, err := Semantic(ctx, pool, fakeQuery{"fake"}, "a query", f, limit)
		if err != nil {
			t.Fatal(err)
		}

		var out []string
		for _, r := range res {
			out = append(out, r.ID)
		}

		return out
	}

	if got := ids(Filters{}, 0); !slices.Equal(got, []string{"OL1W", "OL2W", "OL3W"}) {
		t.Errorf("no filter: %v", got)
	}

	if got := ids(Filters{}, 2); !slices.Equal(got, []string{"OL1W", "OL2W"}) {
		t.Errorf("limit: %v", got)
	}

	if got := ids(Filters{MaxYear: 1970, MinPages: 160}, 0); !slices.Equal(got, []string{"OL1W"}) {
		t.Errorf("year and pages: %v", got)
	}

	if got := ids(Filters{MinYear: 1955, OriginalLanguage: "eng"}, 0); !slices.Equal(got, []string{"OL2W", "OL3W"}) {
		t.Errorf("year and language: %v", got)
	}

	res, err := Semantic(ctx, pool, fakeQuery{"fake"}, "a query", Filters{}, 1)
	if err != nil || len(res) != 1 || !slices.Equal(res[0].Tags, []string{"Thriller", "Oslo"}) || res[0].CoverID != 7 {
		t.Errorf("tags and cover: %+v, %v", res, err)
	}

	if _, err := Semantic(ctx, pool, fakeQuery{"fake"}, " ", Filters{}, 0); !errors.Is(err, ErrEmptyQuery) {
		t.Errorf("empty query: %v", err)
	}
}
