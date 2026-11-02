package shops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// scripted serves its responses in order.
type scripted struct {
	t         *testing.T
	mu        sync.Mutex
	responses []func(w http.ResponseWriter)
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r.Header.Get("User-Agent") != UserAgent || r.Header.Get("Accept") != "application/json" {
		s.t.Errorf("headers: %v", r.Header)
	}

	if len(s.responses) == 0 {
		s.t.Error("unexpected request")
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	next := s.responses[0]
	s.responses = s.responses[1:]
	next(w)
}

func ok(body string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) { fmt.Fprint(w, body) }
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

// testClient returns a client for a server sending responses, and its URL; it records its waits instead of
// sleeping.
func testClient(t *testing.T, responses ...func(w http.ResponseWriter)) (*Client, string, *[]time.Duration) {
	srv := httptest.NewServer(&scripted{t: t, responses: responses})
	t.Cleanup(srv.Close)

	var waits []time.Duration
	c := NewClient()
	c.Pace, c.Backoff, c.MaxBackoff, c.MaxRetries = 0, time.Second, 10*time.Second, 2
	c.Sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}

	return c, srv.URL, &waits
}

func TestDoPaces(t *testing.T) {
	c, url, waits := testClient(t, ok("a"), ok("b"))
	c.Pace = time.Hour
	for _, want := range []string{"a", "b"} {
		if r, err := c.Do(context.Background(), Request{Method: "GET", URL: url}, nil); err != nil || string(r.Body) != want {
			t.Fatalf("got %q, %v", r.Body, err)
		}
	}

	if len(*waits) != 1 || (*waits)[0] < 59*time.Minute {
		t.Errorf("waits %v", *waits)
	}
}

func TestDoRetries(t *testing.T) {
	truncated := func(w http.ResponseWriter) {
		w.Header().Set("Content-Length", "100000")
		fmt.Fprint(w, "cut")
	}
	c, url, waits := testClient(t, status(http.StatusTooManyRequests, "90"), status(http.StatusServiceUnavailable, ""), truncated, ok("done"))
	c.MaxRetries, c.MaxBackoff = 3, time.Hour

	r, err := c.Do(context.Background(), Request{Method: "GET", URL: url}, nil)
	requests, retries := c.Counts()
	if err != nil || string(r.Body) != "done" || requests != 4 || retries != 3 || fmt.Sprint(*waits) != "[1m30s 2s 4s]" {
		t.Errorf("got %q, %v, %d requests, %d retries, waits %v", r.Body, err, requests, retries, *waits)
	}
}

func TestDoCheck(t *testing.T) {
	throttled := errors.New("throttled")
	c, url, waits := testClient(t, ok("throttled"), ok("fine"))
	check := func(r Response) error {
		if string(r.Body) == "throttled" {
			return Retry(throttled)
		}

		return nil
	}

	if r, err := c.Do(context.Background(), Request{Method: "GET", URL: url}, check); err != nil || string(r.Body) != "fine" || len(*waits) != 1 {
		t.Errorf("got %q, %v, waits %v", r.Body, err, *waits)
	}

	c, url, _ = testClient(t, ok("bad"))
	bad := errors.New("bad")
	if _, err := c.Do(context.Background(), Request{Method: "GET", URL: url}, func(Response) error { return bad }); !errors.Is(err, bad) {
		t.Errorf("fatal check: %v", err)
	}
}

func TestDoGivesUp(t *testing.T) {
	c, url, _ := testClient(t, status(429, "3600"), status(429, ""), status(429, ""))
	_, err := c.Do(context.Background(), Request{Method: "GET", URL: url}, nil)
	if requests, _ := c.Counts(); err == nil || !strings.Contains(err.Error(), "429") || requests != 3 {
		t.Errorf("got %v after %d requests", err, requests)
	}

	c, url, waits := testClient(t, status(http.StatusForbidden, ""))
	if _, err := c.Do(context.Background(), Request{Method: "GET", URL: url}, nil); err == nil || len(*waits) != 0 {
		t.Errorf("403: %v, waits %v", err, *waits)
	}
}

func TestDoSendsJSON(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.Header.Get("Content-Type")
	}))
	t.Cleanup(srv.Close)

	if _, err := NewClient().Do(context.Background(), Request{Method: "POST", URL: srv.URL, Body: []byte("{}")}, nil); err != nil || got != "POST application/json" {
		t.Errorf("got %q, %v", got, err)
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
