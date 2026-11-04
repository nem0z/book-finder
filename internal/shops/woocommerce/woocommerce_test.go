package woocommerce

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nem0z/book-finder/internal/shops"
)

// product JSON as the Store API sends it (trimmed).
const (
	hogfather = `{"id":11,"name":"Hogfather: (Discworld Novel 20)","permalink":"https://shop/product/hogfather/","sku":"9780552167581",
		"short_description":"<p>Hogfather: (Discworld Novel 20) | 9780552167581 | Terry Pratchett</p>",
		"description":"<p>It&#8217;s the night before Hogswatch.</p><p>And it&#8217;s too quiet.</p>","is_in_stock":true,
		"images":[{"src":"https://shop/hogfather.jpg"}],"categories":[{"name":"Science Fiction &amp; Fantasy"},{"name":"New Books"}]}`
	bundle = `{"id":12,"name":"Bookseller&#8217;s Choice Bundle","permalink":"https://shop/product/bundle/","sku":"BUNDLE",
		"short_description":"<p>Bookseller&#8217;s Choice Bundle | BUNDLE | </p>","description":"","is_in_stock":true,"images":[],"categories":[]}`
	untitled = `{"id":13,"name":"","permalink":"https://shop/product/open-up-2/","sku":"9780571317059",
		"short_description":"<p> | 9780571317059 | Thomas Morris</p>","description":"","is_in_stock":false,"images":[],"categories":[]}`
)

func TestParseProduct(t *testing.T) {
	c, _ := testCatalog(t, page(2, hogfather))
	var got []shops.Item
	if err := c.List(context.Background(), func(items []shops.Item) error {
		got = append(got, items...)
		return errors.New("first page only")
	}); err == nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}

	want := shops.Book{
		ISBN13: "9780552167581", Title: "Hogfather: (Discworld Novel 20)", Authors: []string{"Terry Pratchett"},
		Genres: []string{"Science Fiction & Fantasy"}, Description: "It’s the night before Hogswatch.\n\nAnd it’s too quiet.",
		URL: "https://shop/product/hogfather/", CoverURL: "https://shop/hogfather.jpg", InStock: true,
	}
	if got[0].Err != nil || !reflect.DeepEqual(got[0].Book, want) {
		t.Errorf("got %+v, %v", got[0].Book, got[0].Err)
	}

	for _, tc := range []struct {
		short   string
		authors []string
	}{
		{"<p>Good Omens | 9780552176019 | Terry Pratchett &amp; Neil Gaiman</p>", []string{"Terry Pratchett", "Neil Gaiman"}},
		{"<p>Title: With | Pipes | 9780552176019 | Anna Burns</p>", []string{"Anna Burns"}},
		{"<p>No author | 9780552176019 | </p>", nil},
		{"", nil},
	} {
		b, err := parseProduct(product{Name: "T", SKU: "9780552176019", ShortDescription: tc.short})
		if err != nil || !reflect.DeepEqual(b.Authors, tc.authors) {
			t.Errorf("%q: got %q, %v", tc.short, b.Authors, err)
		}
	}
}

// testCatalog returns a catalog of a store serving pages in order, and the pages it was asked for.
func testCatalog(t *testing.T, pages ...func(w http.ResponseWriter, n int)) (*Catalog, *[]string) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/wp-json/wc/store/v1/products" || q.Get("per_page") != "100" || q.Get("orderby") != "id" || q.Get("order") != "asc" {
			t.Errorf("request: %s", r.URL)
		}

		asked = append(asked, q.Get("page"))
		if len(asked) > len(pages) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		pages[len(asked)-1](w, len(pages))
	}))
	t.Cleanup(srv.Close)

	c := shops.NewClient()
	c.Pace, c.MaxRetries = 0, 1
	c.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }

	return New(shops.Shop{ID: "test", URL: srv.URL}, c), &asked
}

// page serves products with X-WP-TotalPages = total.
func page(total int, products ...string) func(w http.ResponseWriter, n int) {
	return func(w http.ResponseWriter, _ int) {
		w.Header().Set("X-WP-TotalPages", fmt.Sprint(total))
		fmt.Fprint(w, "["+strings.Join(products, ",")+"]")
	}
}

func TestList(t *testing.T) {
	c, asked := testCatalog(t, page(2, hogfather, bundle), page(2, untitled))
	var got []string
	err := c.List(context.Background(), func(items []shops.Item) error {
		for _, it := range items {
			got = append(got, fmt.Sprint(it.Book.ISBN13, " ", it.Err))
		}

		return nil
	})

	want := []string{"9780552167581 <nil>", " " + shops.ErrNotBook.Error(), " " + shops.ErrNoTitle.Error()}
	if err != nil || fmt.Sprint(got) != fmt.Sprint(want) || fmt.Sprint(*asked) != "[1 2]" {
		t.Errorf("got %q, %v, pages %v", got, err, *asked)
	}
}

func TestListEnds(t *testing.T) {
	ignore := func([]shops.Item) error { return nil }

	// An empty page before the last announced one is an error, not the end of the catalogue.
	c, asked := testCatalog(t, page(5, hogfather), page(5))
	if err := c.List(context.Background(), ignore); err == nil || !strings.Contains(err.Error(), "page 2 of 5 is empty") || len(*asked) != 2 {
		t.Errorf("empty page 2 of 5: %v, pages %v", err, *asked)
	}

	// A store without products announces no pages.
	c, _ = testCatalog(t, page(0))
	if err := c.List(context.Background(), ignore); err != nil {
		t.Errorf("empty store: %v", err)
	}

	for name, resp := range map[string]func(w http.ResponseWriter, n int){
		"no total":  func(w http.ResponseWriter, _ int) { fmt.Fprint(w, "[]") },
		"bad total": func(w http.ResponseWriter, _ int) { w.Header().Set("X-WP-TotalPages", "-1"); fmt.Fprint(w, "[]") },
		"null":      func(w http.ResponseWriter, _ int) { w.Header().Set("X-WP-TotalPages", "5"); fmt.Fprint(w, "null") },
		"not a list": func(w http.ResponseWriter, _ int) {
			w.Header().Set("X-WP-TotalPages", "1")
			fmt.Fprint(w, `{"code":"x"}`)
		},
		"bad JSON": func(w http.ResponseWriter, _ int) { w.Header().Set("X-WP-TotalPages", "1"); fmt.Fprint(w, "<html>") },
	} {
		c, _ := testCatalog(t, resp)
		if err := c.List(context.Background(), ignore); err == nil || !strings.HasPrefix(err.Error(), "woocommerce: ") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
