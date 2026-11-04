package shops

import (
	"bytes"
	"context"
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
	// UserAgent identifies the crawler to the shops, as their robots and agent rules ask.
	UserAgent = "book-finder/1.0 (+https://github.com/nem0z/book-finder)"

	maxErrorBody = 1024
)

// Client sends a shop's requests one at a time, paced, and tries again after rate limits and transient failures.
// Its fields may be changed before use; it is not safe for concurrent use.
type Client struct {
	HTTP      *http.Client
	UserAgent string
	// Pace is the least time between the starts of two requests.
	Pace time.Duration
	// Backoff is the wait before trying a rate-limited or failed request again when the store gives no
	// Retry-After; it doubles at each try, up to MaxBackoff, which also caps Retry-After.
	Backoff, MaxBackoff time.Duration
	// MaxRetries is how many times a request is sent again after a rate limit (HTTP 429), a server error (5xx), a
	// network error, a body cut short, or an answer the caller's check marked with Retry.
	MaxRetries int
	Logger     *slog.Logger
	// Sleep waits d unless ctx ends first; nil sleeps. Tests replace it to record the waits.
	Sleep func(ctx context.Context, d time.Duration) error

	requests, retries int
	last              time.Time
}

// Request is an HTTP request to send; Body is sent as JSON when set.
type Request struct {
	Method, URL string
	Body        []byte
}

// Response is a successful (200) answer.
type Response struct {
	Header http.Header
	Body   []byte
}

// retryError is a failure worth trying again, after the wait the store asked for (0: none given).
type retryError struct {
	err   error
	after time.Duration
}

// NewClient returns a client paced for a long, polite run: a request every 2 s, up to 6 tries again with a
// back-off from 30 s to 5 min.
func NewClient() *Client {
	return &Client{
		HTTP:       &http.Client{Timeout: time.Minute},
		UserAgent:  UserAgent,
		Pace:       2 * time.Second,
		Backoff:    30 * time.Second,
		MaxBackoff: 5 * time.Minute,
		MaxRetries: 6,
	}
}

// Retry marks an error found in a successful answer (e.g. a GraphQL "throttled" error) as worth trying again.
func Retry(err error) error {
	return &retryError{err: err}
}

func (e *retryError) Error() string { return e.err.Error() }
func (e *retryError) Unwrap() error { return e.err }

// Counts returns how many requests the client sent, and how many of them were tries again.
func (c *Client) Counts() (requests, retries int) {
	return c.requests, c.retries
}

// Do sends r and returns its 200 answer, once check (which may be nil) accepts it. It tries again on rate limits,
// server and network errors, bodies cut short and check errors marked with Retry; other failures are returned.
func (c *Client) Do(ctx context.Context, r Request, check func(Response) error) (Response, error) {
	for attempt := 0; ; attempt++ {
		if err := c.wait(ctx, c.Pace-time.Since(c.last)); err != nil {
			return Response{}, err
		}

		c.last = time.Now()
		c.requests++
		resp, err := c.send(ctx, r)
		if err == nil && check != nil {
			err = check(resp)
		}

		var re *retryError
		if err == nil || !errors.As(err, &re) || attempt >= c.MaxRetries {
			return resp, err
		}

		wait := re.after
		if wait == 0 {
			wait = c.Backoff << min(attempt, 16)
		}

		wait = min(wait, c.MaxBackoff)
		c.retries++
		c.logger().Warn("shop request failed, trying again", "url", r.URL, "wait", wait, "attempt", attempt+1, "err", err)
		if werr := c.wait(ctx, wait); werr != nil {
			return Response{}, fmt.Errorf("%w (not retried: %w)", err, werr)
		}
	}
}

// send sends one request. Failures worth trying again are a *retryError.
func (c *Client) send(ctx context.Context, r Request) (Response, error) {
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
	if err != nil {
		return Response{}, err
	}

	if r.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}

		return Response{}, &retryError{err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		err := fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return Response{}, &retryError{err: err, after: retryAfter(resp.Header.Get("Retry-After"))}
		}

		return Response{}, err
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}

		return Response{}, &retryError{err: fmt.Errorf("reading response: %w", err)}
	}

	return Response{Header: resp.Header, Body: data}, nil
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

	if c.Sleep != nil {
		return c.Sleep(ctx, d)
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
