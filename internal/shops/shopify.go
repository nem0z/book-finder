// Package shops reads the catalogue of a bookshop's Shopify store through the tokenless Storefront GraphQL
// API and turns its products into books. See docs/shops.md.
package shops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	SourceChapters = "chapters"
	ChaptersURL    = "https://chaptersbookstore.com"

	APIVersion = "2026-07"
	UserAgent  = "book-finder/1.0 (+https://github.com/nem0z/book-finder)"

	// pageSize is the most products the API returns per request.
	pageSize     = 250
	maxErrorBody = 1024

	productsQuery = `query($first: Int!, $cursor: String) {
  products(first: $first, after: $cursor) {
    pageInfo { hasNextPage endCursor }
    nodes {
      handle title vendor productType descriptionHtml onlineStoreUrl
      featuredImage { url }
      variants(first: 4) { nodes { sku } }
    }
  }
}`
)

// Client walks the products of a Shopify store. Its fields may be changed before use.
type Client struct {
	Source     string // the works source of its books
	BaseURL    string // the store, e.g. https://chaptersbookstore.com
	APIVersion string
	UserAgent  string
	HTTP       *http.Client
	// Pace is the least time between the starts of two requests.
	Pace time.Duration
	// Backoff is the wait before trying a rate-limited or failed request again when the store gives no
	// Retry-After; it doubles at each try, up to MaxBackoff, which also caps Retry-After.
	Backoff, MaxBackoff time.Duration
	// MaxRetries is how many times a request is sent again after a rate limit (HTTP 429 or a THROTTLED
	// GraphQL error), a server error (5xx) or a network error.
	MaxRetries int
	Logger     *slog.Logger

	last  time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

// Stats counts what Walk did.
type Stats struct {
	Requests, Retries int
	Products          int // seen
	Books             int // handed to the callback
	NotBooks          int // skipped: no ISBN-13
	Untitled          int // skipped: no title once cleaned
	Duplicates        int // skipped: ISBN-13 of a book already handed over
}

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type graphQLResponse struct {
	Data *struct {
		Products struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []product `json:"nodes"`
		} `json:"products"`
	} `json:"data"`
	Errors []struct {
		Message    string `json:"message"`
		Extensions struct {
			Code string `json:"code"`
		} `json:"extensions"`
	} `json:"errors"`
}

// retryError is a failure worth trying again, after the wait the store asked for (0: none given).
type retryError struct {
	err   error
	after time.Duration
}

// NewChapters returns a client for Chapters Bookstore (Dublin), paced for a long, polite run.
func NewChapters() *Client {
	return NewClient(SourceChapters, ChaptersURL)
}

// NewClient returns a client for the Shopify store at baseURL, with the default pace and retries.
func NewClient(source, baseURL string) *Client {
	return &Client{
		Source:     source,
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIVersion: APIVersion,
		UserAgent:  UserAgent,
		HTTP:       &http.Client{Timeout: time.Minute},
		Pace:       2 * time.Second,
		Backoff:    30 * time.Second,
		MaxBackoff: 5 * time.Minute,
		MaxRetries: 6,
	}
}

func (e *retryError) Error() string { return e.err.Error() }
func (e *retryError) Unwrap() error { return e.err }

// Walk pages through every product of the store, in order, and hands each page's books to fn with the counts
// so far. It stops at the first error of fn or of a request that cannot be retried. Products that are not
// books, have no title or repeat an ISBN-13 are skipped and counted.
func (c *Client) Walk(ctx context.Context, fn func(books []Book, s Stats) error) (Stats, error) {
	var s Stats
	seen := make(map[string]bool)
	cursor := ""
	for {
		resp, err := c.fetch(ctx, cursor, &s)
		if err != nil {
			return s, err
		}

		page := resp.Data.Products
		books := make([]Book, 0, len(page.Nodes))
		for _, p := range page.Nodes {
			s.Products++
			b, err := parseProduct(p, c.Source, c.BaseURL)
			switch {
			case errors.Is(err, ErrNotBook):
				s.NotBooks++
			case errors.Is(err, ErrNoTitle):
				s.Untitled++
			case seen[b.ISBN13]:
				s.Duplicates++
			default:
				seen[b.ISBN13] = true
				books = append(books, b)
				s.Books++
			}
		}

		if err := fn(books, s); err != nil {
			return s, err
		}

		if !page.PageInfo.HasNextPage {
			return s, nil
		}

		if page.PageInfo.EndCursor == "" {
			return s, errors.New("shopify: next page without a cursor")
		}

		cursor = page.PageInfo.EndCursor
	}
}

// fetch gets the page of products after cursor, trying again on rate limits and transient failures.
func (c *Client) fetch(ctx context.Context, cursor string, s *Stats) (*graphQLResponse, error) {
	vars := map[string]any{"first": pageSize, "cursor": nil}
	if cursor != "" {
		vars["cursor"] = cursor
	}

	body, err := json.Marshal(graphQLRequest{Query: productsQuery, Variables: vars})
	if err != nil {
		return nil, err
	}

	for attempt := 0; ; attempt++ {
		if err := c.wait(ctx, c.Pace-time.Since(c.last)); err != nil {
			return nil, err
		}

		c.last = time.Now()
		s.Requests++
		resp, err := c.post(ctx, body)
		var re *retryError
		if err == nil || !errors.As(err, &re) || attempt >= c.MaxRetries {
			return resp, err
		}

		wait := re.after
		if wait == 0 {
			wait = c.Backoff << min(attempt, 16)
		}

		wait = min(wait, c.MaxBackoff)
		s.Retries++
		c.logger().Warn("shopify request failed, trying again", "wait", wait, "attempt", attempt+1, "err", err)
		if werr := c.wait(ctx, wait); werr != nil {
			return nil, fmt.Errorf("%w (not retried: %w)", err, werr)
		}
	}
}

// post sends one request. Failures worth trying again are a *retryError.
func (c *Client) post(ctx context.Context, body []byte) (*graphQLResponse, error) {
	url := c.BaseURL + "/api/" + c.APIVersion + "/graphql.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		return nil, &retryError{err: fmt.Errorf("shopify: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		err := fmt.Errorf("shopify: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, &retryError{err: err, after: retryAfter(resp.Header.Get("Retry-After"))}
		}

		return nil, err
	}

	var out graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("shopify: bad response: %w", err)
	}

	if len(out.Errors) > 0 {
		msgs := make([]string, len(out.Errors))
		throttled := false
		for i, e := range out.Errors {
			msgs[i] = e.Message
			throttled = throttled || e.Extensions.Code == "THROTTLED"
		}

		err := fmt.Errorf("shopify: GraphQL: %s", strings.Join(msgs, "; "))
		if throttled {
			return nil, &retryError{err: err}
		}

		return nil, err
	}

	if out.Data == nil {
		return nil, errors.New("shopify: bad response: no data")
	}

	return &out, nil
}

// retryAfter reads a Retry-After header, in seconds or as an HTTP date; 0 when absent or invalid.
func retryAfter(h string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}

	if t, err := http.ParseTime(h); err == nil {
		return max(time.Until(t), 0)
	}

	return 0
}

// wait sleeps d, unless ctx ends first.
func (c *Client) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	if c.sleep != nil {
		return c.sleep(ctx, d)
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) logger() *slog.Logger {
	if c.Logger == nil {
		return slog.Default()
	}

	return c.Logger
}
