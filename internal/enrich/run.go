package enrich

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/llm"
)

const (
	// SchemaName names Schema for the LLM provider.
	SchemaName = "book_keywords"

	defaultConcurrency      = 4
	defaultPageSize         = 200
	defaultProgressInterval = 15 * time.Second

	// minFailures is how many failed extractions a run tolerates before it checks whether most fail,
	// which means the LLM settings are wrong rather than a few odd answers.
	minFailures = 10
)

var ErrTooManyFailures = errors.New("most extractions fail; check the LLM settings")

type Options struct {
	// Limit > 0 only considers the Limit most-rated works with a vector.
	Limit int
	// IDs, when set, only considers these works.
	IDs []string
	// Concurrency is how many requests run at once; 0 means 4.
	Concurrency int
	// PerRequest is how many books each request asks about; 0 means 1. See ExtractBatch.
	PerRequest int
	// PerMinute caps the requests sent per minute, to share the deployment's quota; 0 means no cap.
	PerMinute        int
	ProgressInterval time.Duration // default 15s; negative disables progress logs
	Logger           *slog.Logger
}

type Stats struct {
	Considered int64 `json:"considered"` // works with a vector read
	Extracted  int64 `json:"extracted"`  // keywords extracted and stored
	UpToDate   int64 `json:"up_to_date"` // already extracted by the same model and prompt from the same text
	Failed     int64 `json:"failed"`     // extractions that returned an error, skipped
	Pruned     int64 `json:"pruned"`     // keywords deleted because their work is gone
}

type runner struct {
	pool   *pgxpool.Pool
	client llm.Client
	model  string
	opts   Options
	tick   <-chan time.Time // nil without a rate cap
	start  time.Time
	last   time.Time

	mu    sync.Mutex
	stats Stats
}

// Extract asks client for b's keywords.
func Extract(ctx context.Context, client llm.Client, b Book) (Keywords, error) {
	var k Keywords
	if err := client.JSON(ctx, SystemPrompt, UserMessage(b), SchemaName, Schema, &k); err != nil {
		return Keywords{}, fmt.Errorf("%s: %w", b.ID, err)
	}

	return k, nil
}

// Run extracts keywords with client for the works with a vector whose keywords are missing or stale, most
// rated first, then deletes the keywords of works no longer in the catalogue. model is client's model,
// recorded with the keywords. Keywords are stored page by page, so an interrupted run resumes where it
// stopped. A work whose extraction fails is skipped and counted.
func Run(ctx context.Context, pool *pgxpool.Pool, client llm.Client, model string, opts Options) (Stats, error) {
	if opts.Concurrency <= 0 {
		opts.Concurrency = defaultConcurrency
	}

	if opts.PerRequest <= 0 {
		opts.PerRequest = 1
	}

	if opts.ProgressInterval == 0 {
		opts.ProgressInterval = defaultProgressInterval
	}

	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	r := &runner{pool: pool, client: client, model: model, opts: opts, start: time.Now(), last: time.Now()}
	if opts.PerMinute > 0 {
		t := time.NewTicker(time.Minute / time.Duration(opts.PerMinute))
		defer t.Stop()
		r.tick = t.C
	}

	if err := r.extractPending(ctx); err != nil {
		r.report()
		return r.stats, err
	}

	pruned, err := Prune(ctx, pool)
	r.stats.Pruned = pruned
	r.report()

	return r.stats, err
}

func (r *runner) extractPending(ctx context.Context) error {
	var c Cursor
	for size := r.pageSize(); size > 0; size = r.pageSize() {
		p, err := ReadPending(ctx, r.pool, r.model, r.opts.IDs, c, size)
		if err != nil {
			return err
		}

		r.stats.Considered += int64(p.Read)
		r.stats.UpToDate += int64(p.Read - len(p.Books))
		if err := r.extractPage(ctx, p.Books); err != nil {
			return err
		}

		if p.Read < size {
			return nil
		}

		c = p.Next
	}

	return nil
}

// pageSize returns how many works to read next: a full page, fewer near the limit, 0 once it is reached.
// A page holds at least four rounds of requests, so that few workers wait for the slowest at its end.
func (r *runner) pageSize() int {
	size := max(defaultPageSize, 4*r.opts.Concurrency*r.opts.PerRequest)
	if r.opts.Limit <= 0 {
		return size
	}

	return min(size, r.opts.Limit-int(r.stats.Considered))
}

// extractPage extracts books' keywords concurrently, PerRequest books per request, and stores those that
// succeeded.
func (r *runner) extractPage(ctx context.Context, books []Book) error {
	jobs := make(chan []Book)
	var done []Extracted
	var wg sync.WaitGroup
	for range r.opts.Concurrency {
		wg.Go(func() {
			for group := range jobs {
				es := r.extractGroup(ctx, group)
				r.mu.Lock()
				done = append(done, es...)
				r.mu.Unlock()
			}
		})
	}

	err := r.feed(ctx, jobs, slices.Collect(slices.Chunk(books, r.opts.PerRequest)))
	close(jobs)
	wg.Wait()

	// Keep what was extracted, even when ctx was cancelled.
	if serr := Save(context.WithoutCancel(ctx), r.pool, r.model, done); serr != nil {
		return serr
	}

	r.stats.Extracted += int64(len(done))
	if err != nil {
		return err
	}

	if r.stats.Failed >= minFailures && r.stats.Failed > r.stats.Extracted {
		return fmt.Errorf("%w: %d failed, %d extracted", ErrTooManyFailures, r.stats.Failed, r.stats.Extracted)
	}

	r.maybeReport()

	return nil
}

// feed sends groups of books to the workers, one per request at the rate cap, until ctx ends.
func (r *runner) feed(ctx context.Context, jobs chan<- []Book, groups [][]Book) error {
	for _, b := range groups {
		if r.tick != nil {
			select {
			case <-r.tick:
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		select {
		case jobs <- b:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// extractGroup extracts the keywords of a group of books in one request. Books the answer misses are asked
// again one by one, and so are all of them when the request fails or the answer may mix them up. Books
// that still fail are counted.
func (r *runner) extractGroup(ctx context.Context, group []Book) []Extracted {
	ks, err := ExtractBatch(ctx, r.client, group)
	if ctx.Err() != nil {
		return nil
	}

	if err != nil && len(group) > 1 {
		r.opts.Logger.Warn("extraction failed, asking book by book", "books", len(group), "err", err)
	}

	var out []Extracted
	for _, b := range group {
		k, ok := ks[b.ID]
		if !ok && len(group) > 1 {
			var serr error
			if k, serr = Extract(ctx, r.client, b); serr == nil {
				ok = true
			} else {
				err = serr
			}
		}

		if !ok {
			if ctx.Err() != nil {
				return out
			}

			r.opts.Logger.Warn("extraction failed", "work", b.ID, "err", err)
			r.mu.Lock()
			r.stats.Failed++
			r.mu.Unlock()

			continue
		}

		out = append(out, Extracted{WorkID: b.ID, Hash: b.Hash, Keywords: k})
	}

	return out
}
func (r *runner) maybeReport() {
	if r.opts.ProgressInterval > 0 && time.Since(r.last) >= r.opts.ProgressInterval {
		r.report()
		r.last = time.Now()
	}
}

func (r *runner) report() {
	if r.opts.ProgressInterval < 0 {
		return
	}

	rate := float64(r.stats.Extracted) / time.Since(r.start).Minutes()
	r.opts.Logger.Info("extracting keywords", "considered", r.stats.Considered, "extracted", r.stats.Extracted,
		"up_to_date", r.stats.UpToDate, "failed", r.stats.Failed, "pruned", r.stats.Pruned,
		"per_minute", fmt.Sprintf("%.0f", rate))
}
