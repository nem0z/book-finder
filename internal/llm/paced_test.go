package llm

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// scripted answers calls with answer(n), n counting from 1, and records how many run at once and how many
// times it was closed.
type scripted struct {
	answer   func(ctx context.Context, n int64) error
	calls    atomic.Int64
	running  atomic.Int64
	peak     atomic.Int64
	closed   atomic.Int64
	closedAt atomic.Int64 // calls running when it was last closed
}

func (s *scripted) JSON(ctx context.Context, _, _, _ string, _ json.RawMessage, _ any) error {
	n := s.calls.Add(1)
	r := s.running.Add(1)
	defer s.running.Add(-1)
	for p := s.peak.Load(); r > p && !s.peak.CompareAndSwap(p, r); p = s.peak.Load() {
	}

	return s.answer(ctx, n)
}

func (s *scripted) Close() error {
	s.closed.Add(1)
	s.closedAt.Store(s.running.Load())
	return nil
}

func call(p *Paced) error {
	return p.JSON(context.Background(), "s", "u", "n", nil, nil)
}

func TestPacedRetries(t *testing.T) {
	defer func(d time.Duration) { pacedFirstWait = d }(pacedFirstWait)
	pacedFirstWait = time.Millisecond

	// A 429 is tried again, and halves the concurrency.
	s := &scripted{answer: func(_ context.Context, n int64) error {
		if n == 1 {
			return errors.New("unexpected LLM response: 429 Too Many Requests")
		}

		return nil
	}}
	p := &Paced{Client: s, Concurrency: 4, Retries: 2}
	if err := call(p); err != nil || s.calls.Load() != 2 {
		t.Errorf("rate limit: %v, %d calls", err, s.calls.Load())
	}

	if st := p.Stats(); st.RateLimited != 1 || st.Concurrency != 2 || st.Calls != 2 {
		t.Errorf("stats after a rate limit: %+v", st)
	}

	// A timeout is tried again, then reported.
	s = &scripted{answer: func(ctx context.Context, _ int64) error { <-ctx.Done(); return ctx.Err() }}
	p = &Paced{Client: s, Timeout: 5 * time.Millisecond, Retries: 1}
	if err := call(p); !errors.Is(err, ErrTimeout) || s.calls.Load() != 2 || p.Stats().TimedOut != 2 {
		t.Errorf("timeout: %v, %d calls, %+v", err, s.calls.Load(), p.Stats())
	}

	// Other errors are not tried again.
	s = &scripted{answer: func(context.Context, int64) error { return ErrBadResponse }}
	p = &Paced{Client: s, Retries: 3}
	if err := call(p); !errors.Is(err, ErrBadResponse) || s.calls.Load() != 1 {
		t.Errorf("bad answer: %v, %d calls", err, s.calls.Load())
	}

	// A cancelled context stops waiting.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.JSON(ctx, "s", "u", "n", nil, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestPacedConcurrencyAndRate(t *testing.T) {
	s := &scripted{answer: func(context.Context, int64) error { time.Sleep(10 * time.Millisecond); return nil }}
	p := &Paced{Client: s, Concurrency: 3}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() { call(p) }) //nolint:errcheck // always nil
	}

	wg.Wait()
	if peak := s.peak.Load(); peak > 3 || peak < 2 {
		t.Errorf("peak concurrency %d", peak)
	}

	// 600 a minute: one every 100 ms.
	p = &Paced{Client: &scripted{answer: func(context.Context, int64) error { return nil }}, PerMinute: 600}
	start := time.Now()
	for range 3 {
		call(p) //nolint:errcheck // always nil
	}

	if d := time.Since(start); d < 190*time.Millisecond {
		t.Errorf("3 calls at 600 a minute took %v", d)
	}
}

func TestPacedRestart(t *testing.T) {
	// After 2 failures in a row, the client is closed once nothing runs.
	s := &scripted{answer: func(_ context.Context, n int64) error {
		if n <= 2 {
			return ErrBadResponse
		}

		return nil
	}}
	p := &Paced{Client: s, RestartAfter: 2}
	call(p) //nolint:errcheck // fails on purpose
	if s.closed.Load() != 0 {
		t.Error("restarted after one failure")
	}

	call(p) //nolint:errcheck // fails on purpose
	if err := call(p); err != nil || s.closed.Load() != 1 || p.Stats().Restarts != 1 {
		t.Errorf("restart after failures: %v, closed %d", err, s.closed.Load())
	}

	// Every so often, between calls: calls in flight finish first.
	s = &scripted{answer: func(context.Context, int64) error { time.Sleep(5 * time.Millisecond); return nil }}
	p = &Paced{Client: s, RestartEvery: time.Millisecond}
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() { call(p) }) //nolint:errcheck // always nil
	}

	wg.Wait()
	if s.closed.Load() == 0 || s.closedAt.Load() != 0 {
		t.Errorf("periodic restart: closed %d times, with %d calls running", s.closed.Load(), s.closedAt.Load())
	}
}

func TestErrRetryAfter(t *testing.T) {
	for msg, want := range map[string]time.Duration{
		"429 Too Many Requests: Retry-After: 30": 30 * time.Second,
		"rate limit exceeded, retry after 7s":    7 * time.Second,
		"429: please retry after 9999 seconds":   pacedMaxWait,
		"429 Too Many Requests":                  0,
	} {
		if got, _ := errRetryAfter(errors.New(msg)); got != want {
			t.Errorf("%q: %v, want %v", msg, got, want)
		}
	}

	if !isRateLimit(errors.New("Rate limit exceeded")) || isRateLimit(errors.New("bad answer")) {
		t.Error("isRateLimit")
	}
}
