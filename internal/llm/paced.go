package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pacedPoll    = 50 * time.Millisecond
	pacedMaxWait = 2 * time.Minute
	// pacedCalm is how long without a rate limit before the concurrency is raised again, by one.
	pacedCalm = time.Minute
)

var (
	ErrTimeout = errors.New("LLM call timed out")

	// pacedFirstWait is the wait after a first rate limit or timeout without Retry-After.
	pacedFirstWait = 10 * time.Second

	retryAfterRe = regexp.MustCompile(`(?i)retry[- ]after[^0-9]{0,4}([0-9]+)`)
)

// Paced wraps a Client for long runs against a rate-limited service, such as GitHub Copilot. Calls may be
// made from many goroutines; Paced:
//   - starts at most PerMinute calls a minute and runs at most Concurrency at once, when set;
//   - on a rate limit (429), halves the calls it runs at once, then raises them again by one per minute
//     without a rate limit, up to Concurrency;
//   - gives each call at most Timeout, when set;
//   - tries a rate-limited or timed-out call again, up to Retries times, after the wait the error asks for
//     (Retry-After) or 10 s, doubled each time up to 2 minutes;
//   - restarts the client, when it is an io.Closer that starts again on its next call (Copilot), every
//     RestartEvery and after RestartAfter calls failed in a row, when set: new calls wait while the calls in
//     flight finish, then the client is closed.
type Paced struct {
	Client       Client
	Concurrency  int
	PerMinute    int
	Timeout      time.Duration
	Retries      int
	RestartEvery time.Duration
	RestartAfter int
	Logger       *slog.Logger

	mu          sync.Mutex
	init        bool
	limit       int // calls at once now; 0: no limit
	inFlight    int
	nextStart   time.Time
	lastLimited time.Time
	lastRaised  time.Time
	since       time.Time // the client's last (re)start
	failures    int       // in a row
	restarting  bool
	stats       PacedStats
}

// PacedStats counts what Paced did.
type PacedStats struct {
	Calls, RateLimited, TimedOut, Restarts int
	Concurrency                            int // calls at once now; 0: no limit
}

// JSON implements Client.
func (p *Paced) JSON(ctx context.Context, system, user, schemaName string, schema json.RawMessage, out any) error {
	for attempt := 0; ; attempt++ {
		if err := p.acquire(ctx); err != nil {
			return err
		}

		cctx, cancel := ctx, context.CancelFunc(func() {})
		if p.Timeout > 0 {
			cctx, cancel = context.WithTimeout(ctx, p.Timeout)
		}

		err := p.Client.JSON(cctx, system, user, schemaName, schema, out)
		timedOut := err != nil && ctx.Err() == nil && errors.Is(cctx.Err(), context.DeadlineExceeded)
		cancel()
		if timedOut {
			err = fmt.Errorf("%w after %s: %w", ErrTimeout, p.Timeout, err)
		}

		limited := err != nil && isRateLimit(err)
		p.release(err == nil, limited, timedOut)
		if err == nil || ctx.Err() != nil || attempt >= p.Retries || !(limited || timedOut) {
			return err
		}

		wait := min(pacedFirstWait<<attempt, pacedMaxWait)
		if d, ok := errRetryAfter(err); ok {
			wait = d
		}

		p.logger().Warn("LLM call failed, trying again", "wait", wait, "err", err)
		if err := sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// Close closes the client, when it is an io.Closer.
func (p *Paced) Close() error {
	if c, ok := p.Client.(io.Closer); ok {
		return c.Close()
	}

	return nil
}

// Stats returns what Paced did so far.
func (p *Paced) Stats() PacedStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	s := p.stats
	s.Concurrency = p.limit

	return s
}

// acquire waits until a call may start: no restart under way, fewer calls in flight than the limit, and
// the rate cap's next slot reached.
func (p *Paced) acquire(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		p.mu.Lock()
		p.setup()
		wait := pacedPoll
		if !p.restarting && (p.limit == 0 || p.inFlight < p.limit) {
			now := time.Now()
			if p.PerMinute <= 0 || !now.Before(p.nextStart) {
				if p.PerMinute > 0 {
					p.nextStart = now.Add(time.Minute / time.Duration(p.PerMinute))
				}

				p.inFlight++
				p.stats.Calls++
				p.mu.Unlock()

				return nil
			}

			wait = min(wait, p.nextStart.Sub(now))
		}

		p.mu.Unlock()
		if err := sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// release ends a call: it adapts the concurrency to rate limits and restarts the client when it is due.
func (p *Paced) release(ok, limited, timedOut bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.inFlight--
	now := time.Now()
	switch {
	case ok:
		p.failures = 0
		if p.limit > 0 && p.limit < p.Concurrency && now.Sub(p.lastLimited) >= pacedCalm && now.Sub(p.lastRaised) >= pacedCalm {
			p.limit++
			p.lastRaised = now
			p.logger().Info("LLM concurrency raised", "concurrency", p.limit)
		}
	case limited:
		p.failures++
		p.stats.RateLimited++
		p.lastLimited = now
		if p.limit > 1 {
			p.limit = max(1, p.limit/2)
			p.logger().Warn("LLM rate-limited, lowering the concurrency", "concurrency", p.limit)
		}
	default:
		p.failures++
		if timedOut {
			p.stats.TimedOut++
		}
	}

	_, closer := p.Client.(io.Closer)
	if closer && !p.restarting && ((p.RestartEvery > 0 && now.Sub(p.since) >= p.RestartEvery) ||
		(p.RestartAfter > 0 && p.failures >= p.RestartAfter)) {
		p.restarting = true
	}

	if p.restarting && p.inFlight == 0 {
		p.restart()
	}
}

// restart closes the client, which starts again on its next call. Called with p.mu held and no call in
// flight.
func (p *Paced) restart() {
	if err := p.Client.(io.Closer).Close(); err != nil {
		p.logger().Warn("LLM client restart", "err", err)
	}

	p.stats.Restarts++
	p.restarting, p.failures, p.since = false, 0, time.Now()
	p.logger().Info("LLM client restarted", "restarts", p.stats.Restarts)
}

// setup sets the starting state on first use. Called with p.mu held.
func (p *Paced) setup() {
	if p.init {
		return
	}

	p.init, p.limit, p.since = true, max(p.Concurrency, 0), time.Now()
}

func (p *Paced) logger() *slog.Logger {
	if p.Logger == nil {
		return slog.Default()
	}

	return p.Logger
}

// isRateLimit reports whether err says the service is rate-limiting.
func isRateLimit(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "429") || strings.Contains(s, "rate limit") || strings.Contains(s, "too many requests")
}

// errRetryAfter reads the wait in seconds a rate-limit error asks for ("Retry-After: 30", "retry after 30s"),
// at most pacedMaxWait.
func errRetryAfter(err error) (time.Duration, bool) {
	m := retryAfterRe.FindStringSubmatch(err.Error())
	if m == nil {
		return 0, false
	}

	s, cerr := strconv.Atoi(m[1])
	if cerr != nil || s <= 0 {
		return 0, false
	}

	return min(time.Duration(s)*time.Second, pacedMaxWait), true
}
