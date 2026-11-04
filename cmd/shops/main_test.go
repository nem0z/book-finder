package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/shops"
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

// testReader returns a reader of a Shopify store serving two pages of one book each; the second page fails with
// status when it is not 200. It counts the requests.
func testReader(t *testing.T, status int) (shops.Reader, *atomic.Int32) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			fmt.Fprint(w, storefrontPage(true, 1, "9781472277299", "Where the Crawdads Sing [2019] paperback"))
			return
		}

		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}

		fmt.Fprint(w, storefrontPage(false, 2, "9780008742775", "Verity"))
	}))
	t.Cleanup(srv.Close)

	c := shops.NewClient()
	c.Pace = 0
	r, err := reader(shops.Shop{ID: "chapters", URL: srv.URL, Platform: shops.Shopify}, c)
	if err != nil {
		t.Fatal(err)
	}

	return r, &requests
}

func storefrontPage(next bool, id int, isbn, title string) string {
	return fmt.Sprintf(`{"data":{"products":{"pageInfo":{"hasNextPage":%t},"nodes":[{"id":"gid://shopify/Product/%d",
		"handle":"h","title":%q,"vendor":"Owens, Delia","productType":"FICTION","descriptionHtml":"<p>A marsh girl.</p>",
		"variants":{"nodes":[{"sku":%q}]}}]}}}`, next, id, title, isbn)
}

func workIDs(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var ids []string
	err := pool.QueryRow(context.Background(), `SELECT array_agg(id ORDER BY id COLLATE "C") FROM works`).Scan(&ids)
	if err != nil {
		t.Fatal(err)
	}

	return strings.Join(ids, " ")
}

func TestLoad(t *testing.T) {
	pool := testPool(t)
	r, _ := testReader(t, http.StatusOK)
	if err := load(context.Background(), pool, r, options{}); err != nil {
		t.Fatal(err)
	}

	if got := workIDs(t, pool); got != "OL1W chapters:9780008742775 chapters:9781472277299" {
		t.Errorf("works: %s", got)
	}
}

func TestLoadUnchanged(t *testing.T) {
	for name, tc := range map[string]struct {
		status, maxPages int
		wantErr          bool
		wantRequests     int32
	}{
		"failed":             {status: http.StatusBadRequest, wantErr: true, wantRequests: 2},
		"trial":              {status: http.StatusOK, maxPages: 1, wantRequests: 1},
		"trial past the end": {status: http.StatusOK, maxPages: 3, wantRequests: 2},
		"negative trial":     {status: http.StatusOK, maxPages: -1, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			pool := testPool(t)
			r, requests := testReader(t, tc.status)
			err := load(context.Background(), pool, r, options{maxPages: tc.maxPages})
			if (err != nil) != tc.wantErr || requests.Load() != tc.wantRequests {
				t.Errorf("err %v after %d requests", err, requests.Load())
			}

			if got := workIDs(t, pool); got != "OL1W chapters:9780141439518" {
				t.Errorf("works changed: %s", got)
			}

			var stage *string
			if err := pool.QueryRow(context.Background(), `SELECT to_regclass('stage_works_chapters')::text`).Scan(&stage); err != nil || stage != nil {
				t.Errorf("stage left behind: %v", err)
			}
		})
	}
}

func TestSelectShops(t *testing.T) {
	all, err := selectShops("all")
	if err != nil || len(all) != len(shops.All) {
		t.Errorf("all: %v, %v", all, err)
	}

	if got, err := selectShops("chapters"); err != nil || len(got) != 1 || got[0].ID != "chapters" {
		t.Errorf("chapters: %v, %v", got, err)
	}

	if _, err := selectShops("hodgesfiggis"); !errors.Is(err, shops.ErrUnknownShop) {
		t.Errorf("unknown: %v", err)
	}
}

// TestReader checks that every shop has a reader on its platform.
func TestReader(t *testing.T) {
	for _, s := range shops.All {
		if _, err := reader(s, shops.NewClient()); err != nil {
			t.Error(err)
		}
	}

	if _, err := reader(shops.Shop{ID: "x", Platform: "magento"}, shops.NewClient()); err == nil {
		t.Error("unknown platform: no error")
	}
}
