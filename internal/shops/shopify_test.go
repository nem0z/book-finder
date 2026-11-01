package shops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// scripted serves its responses in order, recording the requests' cursors.
type scripted struct {
	t         *testing.T
	mu        sync.Mutex
	responses []func(w http.ResponseWriter)
	queries   []any
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r.Method != http.MethodPost || r.URL.Path != "/api/"+APIVersion+"/graphql.json" ||
		r.Header.Get("Content-Type") != "application/json" || r.Header.Get("User-Agent") != UserAgent {
		s.t.Errorf("request: %s %s %v", r.Method, r.URL.Path, r.Header)
	}

	var req graphQLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Variables["first"] != float64(pageSize) {
		s.t.Errorf("body: %+v, %v", req, err)
	}

	s.queries = append(s.queries, req.Variables["query"])
	if len(s.responses) == 0 {
		s.t.Error("unexpected request")
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	next := s.responses[0]
	s.responses = s.responses[1:]
	next(w)
}

// page serves products with IDs from firstID up, made of "sku|title" pairs.
func page(firstID int, hasNext bool, titles ...string) func(w http.ResponseWriter) {
	nodes := make([]string, len(titles))
	for i, t := range titles {
		sku, title, _ := strings.Cut(t, "|")
		nodes[i] = fmt.Sprintf(`{"id":"gid://shopify/Product/%d","handle":"h%d","title":%q,"vendor":"Owens, Delia","productType":"FICTION","descriptionHtml":"<p>x</p>","featuredImage":{"url":"https://cdn/c.jpg"},"variants":{"nodes":[{"sku":%q}]}}`, firstID+i, i, title, sku)
	}

	return func(w http.ResponseWriter) {
		fmt.Fprintf(w, `{"data":{"products":{"pageInfo":{"hasNextPage":%t},"nodes":[%s]}}}`, hasNext, strings.Join(nodes, ","))
	}
}

func status(code int, retryAfter string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}

		w.WriteHeader(code)
		fmt.Fprint(w, "slow down")
	}
}

func graphQLError(code string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		fmt.Fprintf(w, `{"errors":[{"message":"failed %s","extensions":{"code":%q}}]}`, code, code)
	}
}

// testClient returns a client for a store serving responses; it records its waits instead of sleeping.
func testClient(t *testing.T, responses ...func(w http.ResponseWriter)) (*Client, *scripted, *[]time.Duration) {
	s := &scripted{t: t, responses: responses}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	var waits []time.Duration
	c := NewClient("test", srv.URL)
	c.Pace, c.Backoff, c.MaxBackoff, c.MaxRetries = 0, time.Second, 10*time.Second, 2
	c.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}

	return c, s, &waits
}

func TestWalkPages(t *testing.T) {
	c, s, waits := testClient(t,
		page(7248452387001, true, "9781472277299|Where the Crawdads Sing: Delia Owens [2019] paperback", "4260110581646|Some DVD"),
		page(7248452387005, false, "9780008742775|9780008742775 [2024]", "XG81472277299|Again", "9780008742775|Verity [2018]"),
	)
	c.Pace = time.Hour

	var got []string
	stats, err := c.Walk(context.Background(), func(books []Book, s Stats) error {
		for _, b := range books {
			got = append(got, b.ID()+" "+b.Title+" "+b.URL+" "+b.CoverURL)
		}

		return nil
	})

	want := []string{
		"test:9781472277299 Where the Crawdads Sing " + c.BaseURL + "/products/h0 https://cdn/c.jpg",
		"test:9780008742775 Verity " + c.BaseURL + "/products/h2 https://cdn/c.jpg",
	}
	wantStats := Stats{Requests: 2, Products: 5, Books: 2, NotBooks: 1, Untitled: 1, Duplicates: 1}
	if err != nil || fmt.Sprint(got) != fmt.Sprint(want) || stats != wantStats {
		t.Errorf("got %q, %+v, %v", got, stats, err)
	}

	if fmt.Sprint(s.queryList()) != "[<nil> id:>7248452387002]" || len(*waits) != 1 || (*waits)[0] < 59*time.Minute {
		t.Errorf("queries %v, waits %v", s.queryList(), *waits)
	}
}

// TestWalkEnds checks that paging stops on an empty page, and fails on IDs that cannot drive the next query.
func TestWalkEnds(t *testing.T) {
	ids := func(ids ...string) func(w http.ResponseWriter) {
		return func(w http.ResponseWriter) {
			nodes := make([]string, len(ids))
			for i, id := range ids {
				nodes[i] = fmt.Sprintf(`{"id":%q,"variants":{"nodes":[]}}`, id)
			}

			fmt.Fprintf(w, `{"data":{"products":{"pageInfo":{"hasNextPage":true},"nodes":[%s]}}}`, strings.Join(nodes, ","))
		}
	}

	for name, tc := range map[string]struct {
		responses []func(w http.ResponseWriter)
		err       string
	}{
		"empty page":       {[]func(w http.ResponseWriter){ids("gid://shopify/Product/5"), ids()}, ""},
		"bad ID":           {[]func(w http.ResponseWriter){ids("gid://shopify/Product/x")}, "bad product ID"},
		"no ID":            {[]func(w http.ResponseWriter){ids("")}, "bad product ID"},
		"ID not increased": {[]func(w http.ResponseWriter){ids("gid://shopify/Product/5"), ids("gid://shopify/Product/5")}, "do not increase"},
	} {
		c, s, _ := testClient(t, tc.responses...)
		_, err := c.Walk(context.Background(), func([]Book, Stats) error { return nil })
		if (tc.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.err)) || len(s.queryList()) != len(tc.responses) {
			t.Errorf("%s: got %v, queries %v", name, err, s.queryList())
		}
	}
}

func (s *scripted) queryList() []any {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.queries
}

func TestWalkRetries(t *testing.T) {
	c, _, waits := testClient(t,
		status(http.StatusTooManyRequests, "90"),
		status(http.StatusServiceUnavailable, ""),
		page(1, false),
	)
	c.MaxBackoff = time.Hour

	stats, err := c.Walk(context.Background(), func([]Book, Stats) error { return nil })
	if err != nil || stats.Requests != 3 || stats.Retries != 2 || fmt.Sprint(*waits) != "[1m30s 2s]" {
		t.Errorf("got %+v, %v, waits %v", stats, err, *waits)
	}
}

func TestWalkTruncatedBody(t *testing.T) {
	truncated := func(w http.ResponseWriter) {
		w.Header().Set("Content-Length", "100000")
		fmt.Fprint(w, `{"data":{"products":{"pageInfo":`)
	}
	c, _, waits := testClient(t, truncated, page(1, false, "9781472277299|Persians [2022]"))

	stats, err := c.Walk(context.Background(), func([]Book, Stats) error { return nil })
	if err != nil || stats.Requests != 2 || stats.Retries != 1 || stats.Books != 1 || fmt.Sprint(*waits) != "[1s]" {
		t.Errorf("got %+v, %v, waits %v", stats, err, *waits)
	}
}

func TestWalkThrottled(t *testing.T) {
	c, _, waits := testClient(t, graphQLError("THROTTLED"), graphQLError("THROTTLED"), page(1, false))

	stats, err := c.Walk(context.Background(), func([]Book, Stats) error { return nil })
	if err != nil || stats.Retries != 2 || fmt.Sprint(*waits) != "[1s 2s]" {
		t.Errorf("got %+v, %v, waits %v", stats, err, *waits)
	}
}

func TestWalkGivesUp(t *testing.T) {
	c, _, _ := testClient(t, status(429, "3600"), status(429, ""), status(429, ""))

	stats, err := c.Walk(context.Background(), func([]Book, Stats) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "429") || stats.Requests != 3 {
		t.Errorf("got %+v, %v", stats, err)
	}
}

func TestWalkFatal(t *testing.T) {
	for name, resp := range map[string]func(w http.ResponseWriter){
		"GraphQL error": graphQLError("ACCESS_DENIED"),
		"HTTP 403":      status(http.StatusForbidden, ""),
		"bad JSON":      func(w http.ResponseWriter) { fmt.Fprint(w, "<html>") },
		"no data":       func(w http.ResponseWriter) { fmt.Fprint(w, "{}") },
	} {
		c, _, waits := testClient(t, resp)
		stats, err := c.Walk(context.Background(), func([]Book, Stats) error { return nil })
		if err == nil || stats.Requests != 1 || len(*waits) != 0 {
			t.Errorf("%s: got %+v, %v", name, stats, err)
		}
	}
}

func TestWalkStopsOnCallbackError(t *testing.T) {
	c, _, _ := testClient(t, page(1, true, "9781472277299|Persians [2022]"))
	stop := errors.New("stop")

	if _, err := c.Walk(context.Background(), func([]Book, Stats) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("got %v", err)
	}
}

func TestRetryAfter(t *testing.T) {
	date := time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)
	for in, want := range map[string]time.Duration{"": 0, "7": 7 * time.Second, "-1": 0, "soon": 0} {
		if got := retryAfter(in); got != want {
			t.Errorf("%q: got %v", in, got)
		}
	}

	if got := retryAfter(date); got < 50*time.Second || got > time.Minute {
		t.Errorf("date: got %v", got)
	}
}
