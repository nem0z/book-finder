package shopify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nem0z/book-finder/internal/shops"
)

// store serves its responses in order, recording the requests' "query" variables.
type store struct {
	t         *testing.T
	mu        sync.Mutex
	responses []string
	queries   []any
}

func (s *store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var req graphQLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || r.Method != http.MethodPost ||
		r.URL.Path != "/api/"+APIVersion+"/graphql.json" || req.Variables["first"] != float64(pageSize) {
		s.t.Errorf("request: %s %s %+v, %v", r.Method, r.URL.Path, req, err)
	}

	s.queries = append(s.queries, req.Variables["query"])
	if len(s.responses) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	fmt.Fprint(w, s.responses[0])
	s.responses = s.responses[1:]
}

// page is a products page with IDs from firstID up, made of "sku|title" pairs.
func page(firstID int, hasNext bool, titles ...string) string {
	nodes := make([]string, len(titles))
	for i, t := range titles {
		sku, title, _ := strings.Cut(t, "|")
		nodes[i] = fmt.Sprintf(`{"id":"gid://shopify/Product/%d","handle":"h%d","title":%q,"vendor":"Owens, Delia","productType":"FICTION","descriptionHtml":"<p>x</p>","availableForSale":true,"featuredImage":{"url":"https://cdn/c.jpg"},"variants":{"nodes":[{"sku":%q}]}}`, firstID+i, i, title, sku)
	}

	return fmt.Sprintf(`{"data":{"products":{"pageInfo":{"hasNextPage":%t},"nodes":[%s]}}}`, hasNext, strings.Join(nodes, ","))
}

func testCatalog(t *testing.T, responses ...string) (*Catalog, *store) {
	s := &store{t: t, responses: responses}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	c := shops.NewClient()
	c.Pace, c.MaxRetries = 0, 1
	c.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }

	return New(shops.Shop{ID: "test", URL: srv.URL + "/"}, c), s
}

func list(c *Catalog) ([]string, error) {
	var got []string
	err := c.List(context.Background(), func(items []shops.Item) error {
		for _, it := range items {
			got = append(got, fmt.Sprint(it.Book.ISBN13, " ", it.Book.Title, " ", it.Book.InStock, " ", it.Err))
		}

		return nil
	})

	return got, err
}

func TestList(t *testing.T) {
	c, s := testCatalog(t,
		page(7248452387001, true, "9781472277299|Where the Crawdads Sing: Delia Owens [2019] paperback", "4260110581646|Some DVD"),
		page(7248452387005, false, "9780008742775|Verity [2018]"),
	)

	got, err := list(c)
	want := []string{
		"9781472277299 Where the Crawdads Sing true <nil>",
		"  false " + shops.ErrNotBook.Error(),
		"9780008742775 Verity true <nil>",
	}
	if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %q, %v", got, err)
	}

	if fmt.Sprint(s.queries) != "[<nil> id:>7248452387002]" {
		t.Errorf("queries %v", s.queries)
	}
}

// TestListEnds checks that paging stops on an empty page, and fails on IDs that cannot drive the next query.
func TestListEnds(t *testing.T) {
	ids := func(ids ...string) string {
		nodes := make([]string, len(ids))
		for i, id := range ids {
			nodes[i] = fmt.Sprintf(`{"id":%q,"variants":{"nodes":[]}}`, id)
		}

		return fmt.Sprintf(`{"data":{"products":{"pageInfo":{"hasNextPage":true},"nodes":[%s]}}}`, strings.Join(nodes, ","))
	}

	for name, tc := range map[string]struct {
		responses []string
		err       string
	}{
		"empty page":       {[]string{ids("gid://shopify/Product/5"), ids()}, ""},
		"bad ID":           {[]string{ids("gid://shopify/Product/x")}, "bad product ID"},
		"ID not increased": {[]string{ids("gid://shopify/Product/5"), ids("gid://shopify/Product/5")}, "do not increase"},
	} {
		c, s := testCatalog(t, tc.responses...)
		_, err := list(c)
		if (tc.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.err)) || len(s.queries) != len(tc.responses) {
			t.Errorf("%s: got %v, queries %v", name, err, s.queries)
		}
	}
}

func TestListGraphQLErrors(t *testing.T) {
	throttled := `{"errors":[{"message":"slow","extensions":{"code":"THROTTLED"}}]}`
	c, _ := testCatalog(t, throttled, page(1, false))
	if _, err := list(c); err != nil {
		t.Errorf("throttled once: %v", err)
	}

	for name, resp := range map[string]string{
		"denied":   `{"errors":[{"message":"no","extensions":{"code":"ACCESS_DENIED"}}]}`,
		"bad JSON": "<html>",
		"no data":  "{}",
	} {
		c, s := testCatalog(t, resp, page(1, false))
		if _, err := list(c); err == nil || !strings.HasPrefix(err.Error(), "shopify: ") || len(s.queries) != 1 {
			t.Errorf("%s: got %v after %d requests", name, err, len(s.queries))
		}
	}
}

func TestCleanTitle(t *testing.T) {
	for _, tc := range []struct {
		raw, vendor, title string
		year               int
		format             string
	}{
		{"Professor Lloyd Llewellyn-Jones: Persians: Tpb W2 [2022] paperback", "Llewellyn-Jones, Professor Lloyd", "Persians", 2022, "paperback"},
		{"Where the Crawdads Sing: Delia Owens [2019] paperback", "Owens, Delia", "Where the Crawdads Sing", 2019, "paperback"},
		{"Gabrielle Zevin: Tomorrow, and Tomorrow, and Tomorrow [2023] paperback", "Zevin, Gabrielle", "Tomorrow, and Tomorrow, and Tomorrow", 2023, "paperback"},
		{"Colleen Hoover: 9780008742775 [2024] paperback", "Hoover, Colleen", "9780008742775", 2024, "paperback"},
		{"Virginia Evans: The Correspondent [2026]", "Evans, Virginia", "The Correspondent", 2026, ""},
		{"Knapp Florence: The Names [2026]", "Florence Knapp", "The Names", 2026, ""},
		{"McConaghy Charlotte: Wild Dark Shore [2025]", "Mcconaghy, Charlotte", "Wild Dark Shore", 2025, ""},
		{"Fannie Flagg: CANT WAIT TO GET TO HEAVEN [2007] paperback", "Flagg, Fannie", "Cant Wait To Get To Heaven", 2007, "paperback"},
		{"Bridgerton: To Sir Phillip, With Love (Bridgertons Book 5): Inspiration for the Netflix Original Series Bridgerton: Eloise's story (Bridgerton Family) [2021] pa", "Quinn, Julia",
			"Bridgerton: To Sir Phillip, With Love (Bridgertons Book 5): Inspiration for the Netflix Original Series Bridgerton: Eloise's story (Bridgerton Family)", 2021, "paperback"},
		{"Some Very Long Title That Was Cut [20", "Unknown", "Some Very Long Title That Was Cut", 0, ""},
		{"Anna Burns: Milkman [2018] hardback", "", "Anna Burns: Milkman", 2018, "hardback"},
		{"Confusion Of Karen Carpenter Z90 [2013] paperback", "Harvey, Jonathan", "Confusion Of Karen Carpenter", 2013, "paperback"},
		{"Agatha Christie: They Do It With Mirrors [2003]", "Christie, Agatha & Hickson, Joan", "They Do It With Mirrors", 2003, ""},
		{"Catch-22 [1994] paperback", "Heller, Joseph", "Catch-22", 1994, "paperback"},
	} {
		title, year, format := cleanTitle(tc.raw, shops.AuthorNames(tc.vendor))
		if title != tc.title || year != tc.year || format != tc.format {
			t.Errorf("%q: got %q, %d, %q", tc.raw, title, year, format)
		}
	}
}

func TestProductISBN(t *testing.T) {
	for _, tc := range []struct {
		skus []string
		want string
	}{
		{[]string{"9781472277299", "XG81472277299"}, "9781472277299"},
		{[]string{"XV81472277299"}, "9781472277299"},
		{[]string{" 9780008742775 "}, "9780008742775"},
		{[]string{"4260110581646"}, ""}, // a DVD's EAN
		{[]string{"XG81472277298"}, ""}, // bad check digit
		{nil, ""},
	} {
		var p product
		for _, s := range tc.skus {
			p.Variants.Nodes = append(p.Variants.Nodes, variant{s})
		}

		got, err := productISBN(p)
		if got != tc.want || (tc.want == "") != errors.Is(err, shops.ErrNotBook) {
			t.Errorf("%q: got %q, %v", tc.skus, got, err)
		}
	}
}

func TestGenre(t *testing.T) {
	for in, want := range map[string]string{
		"FICTION PAPERBACK": "Fiction paperback", "BARGAIN FICTION PAPERBACK": "Fiction paperback",
		"IRISH FICTION": "Irish fiction", "BARGAIN HISTORY": "History", "": "",
	} {
		if got := genre(in); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
}

func TestParseProduct(t *testing.T) {
	p := product{
		Handle:           "persians",
		Title:            "Professor Lloyd Llewellyn-Jones: Persians: Tpb W2 [2022] paperback",
		Vendor:           "Llewellyn-Jones, Professor Lloyd",
		ProductType:      "HISTORY",
		DescriptionHTML:  "<p>A history.</p>",
		AvailableForSale: true,
	}
	p.Variants.Nodes = []variant{{"9781472277299"}}

	got, err := parseProduct(p, "https://shop.example")
	want := shops.Book{
		ISBN13: "9781472277299", Title: "Persians", Authors: []string{"Professor Lloyd Llewellyn-Jones"},
		Year: 2022, Format: "paperback", Genres: []string{"History"}, Description: "A history.",
		URL: "https://shop.example/products/persians", InStock: true,
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, %v", got, err)
	}

	p.Title, p.Vendor = "Colleen Hoover: 9780008742775 [2024] paperback", "Hoover, Colleen"
	if _, err := parseProduct(p, ""); !errors.Is(err, shops.ErrNoTitle) {
		t.Errorf("ISBN as title: got %v", err)
	}
}
