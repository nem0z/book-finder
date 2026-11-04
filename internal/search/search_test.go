package search

import (
	"context"
	"errors"
	"os"
	"reflect"
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

	if _, err := pool.Exec(ctx, "TRUNCATE works, work_authors, authors, work_keyword_embeddings"); err != nil {
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

func TestKeywordVectors(t *testing.T) {
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

		if _, err := pool.Exec(ctx, "INSERT INTO work_keyword_embeddings (work_id, model, content_hash, embedding) VALUES ($1, $2, 'h', $3::halfvec)",
			id, model, embed.VectorLiteral(v)); err != nil {
			t.Fatal(err)
		}
	}

	ids := func(f Filters, limit int) []string {
		t.Helper()
		res, err := KeywordVectors(ctx, pool, fakeQuery{"fake"}, "a query", f, limit)
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

	res, err := KeywordVectors(ctx, pool, fakeQuery{"fake"}, "a query", Filters{}, 1)
	if err != nil || len(res) != 1 || !slices.Equal(res[0].Tags, []string{"Thriller", "Oslo"}) || res[0].CoverID != 7 {
		t.Errorf("tags and cover: %+v, %v", res, err)
	}

	if _, err := KeywordVectors(ctx, pool, fakeQuery{"fake"}, " ", Filters{}, 0); !errors.Is(err, ErrEmptyQuery) {
		t.Errorf("empty query: %v", err)
	}

	// The engine re-ranks the retrieved works: here, the last one wins.
	e := &Engine{Pool: pool, Embedder: fakeQuery{"fake"}, Reranker: &fakeReranker{scores: []float64{0, 0, 1}}}
	if res, err := e.Search(ctx, "a query", Filters{}, 1); err != nil || len(res) != 1 || res[0].ID != "OL3W" {
		t.Errorf("re-ranked: %+v, %v", res, err)
	}

	if res, err := e.Retrieve(ctx, "a query", Filters{}, 10); err != nil || len(res) != 3 || res[0].ID != "OL1W" {
		t.Errorf("retrieved: %+v, %v", res, err)
	}
}

// TestKeywordVectorsInShop also checks a shop work whose description a refresh removed while its keyword vector
// remains: it must not break the search.
func TestKeywordVectorsInShop(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO works (id, title, description, isbn_13) VALUES ('OL1W', 'Open Library', 'd', '9780000000001');
		INSERT INTO works (id, title, description, isbn_13, source, source_url, cover_url, in_stock) VALUES
		('chapters:9780000000001', 'Bookshop', 'd', '9780000000001', 'chapters', 'https://chapters/p/1', 'https://cdn/1.jpg', true),
		('booksupstairs:9780000000001', 'Bookshop', 'd', '9780000000001', 'booksupstairs', 'https://upstairs/p/1', NULL, false),
		('booksupstairs:9780000000002', 'Other', NULL, '9780000000002', 'booksupstairs', 'https://upstairs/p/2', NULL, NULL)`); err != nil {
		t.Fatal(err)
	}

	for id, v := range map[string][]float32{"OL1W": axis(1, 1), "chapters:9780000000001": axis(1, 0.5),
		"booksupstairs:9780000000001": axis(1, 0.4), "booksupstairs:9780000000002": axis(2, 1)} {
		if _, err := pool.Exec(ctx, "INSERT INTO work_keyword_embeddings (work_id, model, content_hash, embedding) VALUES ($1, 'fake', 'h', $2::halfvec)",
			id, embed.VectorLiteral(v)); err != nil {
			t.Fatal(err)
		}
	}

	both := []Offer{{"booksupstairs", "https://upstairs/p/1", false}, {"chapters", "https://chapters/p/1", true}}
	all, err := KeywordVectors(ctx, pool, fakeQuery{"fake"}, "a query", Filters{}, 0)
	if err != nil || len(all) != 3 || all[0].ID != "OL1W" || all[0].SourceURL != "" || !reflect.DeepEqual(all[0].Shops, both) {
		t.Fatalf("all: %+v, %v", all, err)
	}

	// The book at both shops comes back once, as the nearest of its two works.
	shop, err := KeywordVectors(ctx, pool, fakeQuery{"fake"}, "a query", Filters{InShop: true}, 0)
	if err != nil || len(shop) != 2 {
		t.Fatalf("in shop: %+v, %v", shop, err)
	}

	if r := shop[0]; r.ID != "chapters:9780000000001" || r.SourceURL != "https://chapters/p/1" || r.CoverURL != "https://cdn/1.jpg" ||
		!reflect.DeepEqual(r.Shops, both) {
		t.Errorf("bookshop result: %+v", r)
	}

	if r := shop[1]; r.ID != "booksupstairs:9780000000002" || !reflect.DeepEqual(r.Shops, []Offer{{"booksupstairs", "https://upstairs/p/2", false}}) {
		t.Errorf("one shop: %+v", r)
	}
}
