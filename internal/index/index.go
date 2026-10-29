// Package index makes the catalogue searchable: it has an LLM extract each eligible work's keywords
// (internal/enrich) and embeds them (internal/embed), in one streaming step. Requests for several works run
// at once, and each work's keyword vector is embedded as soon as its keywords arrive, so the step takes
// about as long as the slower of the two.
package index

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/embed"
	"github.com/nem0z/book-finder/internal/enrich"
	"github.com/nem0z/book-finder/internal/llm"
)

const (
	// MinDescription is the shortest description, in characters, worth indexing: shorter ones say too
	// little about the story.
	MinDescription = 300

	defaultConcurrency      = 4
	defaultPerRequest       = 5
	defaultAttempts         = 3
	defaultEmbedBatch       = 32
	defaultProgressInterval = 30 * time.Second

	// embedWait is how long the embedder waits for a full batch before embedding what it has.
	embedWait = 2 * time.Second

	// minFailures is how many failed works a run tolerates before it checks whether most fail, which means
	// the LLM settings are wrong rather than a few odd answers.
	minFailures = 10

	// maxListedFailures is how many failed works the final log names.
	maxListedFailures = 50
)

// pendingSQL reads eligible works, most-rated first, after the ($1, $2) position in that order: a
// description of at least $3 characters without links or HTML (those are usually series lists or markup,
// not a summary). With each, the hash of its keywords, their text (as enrich.Keywords.Text joins them) and
// the hash of its keyword vector.
const pendingSQL = `
SELECT w.id, w.title, w.author_names, w.description, w.rating_count,
       coalesce(k.content_hash, ''),
       coalesce(array_to_string(k.keywords || k.setting_places || k.themes ||
                                array_remove(ARRAY[k.era_label, k.protagonist], ''), '; '), ''),
       coalesce(e.content_hash, '')
FROM works w
LEFT JOIN work_keywords k ON k.work_id = w.id
LEFT JOIN work_keyword_embeddings e ON e.work_id = w.id
WHERE length(w.description) >= $3
  AND w.description !~ '(https?://|\[[0-9]+\]:|<[a-zA-Z]+[ >/])'
  AND (-w.rating_count, w.id) > ($1, $2)
ORDER BY -w.rating_count, w.id
LIMIT $4`

var (
	ErrTooManyFailures = errors.New("most extractions fail; check the LLM settings")

	// pageSize is how many works are read at once.
	pageSize = 5000
)

type Options struct {
	// Limit > 0 only considers the Limit most-rated eligible works.
	Limit int
	// Concurrency is how many LLM requests run at once (0: 4), PerRequest how many works each asks about
	// (0: 5), Attempts how many rounds a work gets before it is left for the next run (0: 3; see
	// enrich.ExtractGroup).
	Concurrency, PerRequest, Attempts int
	// EmbedBatch is how many keyword texts each embedding call takes; 0 means 32.
	EmbedBatch int
	// DryRun only counts what a run would do.
	DryRun           bool
	ProgressInterval time.Duration // default 30s; negative disables progress logs
	Logger           *slog.Logger
}

type Stats struct {
	Considered int64 `json:"considered"` // eligible works read
	UpToDate   int64 `json:"up_to_date"` // keywords and keyword vector current
	ToExtract  int64 `json:"to_extract"` // keywords missing or stale: extracted, then embedded
	ToEmbed    int64 `json:"to_embed"`   // keywords current, keyword vector missing or stale
	Extracted  int64 `json:"extracted"`
	Embedded   int64 `json:"embedded"`
	Failed     int64 `json:"failed"` // extractions that failed every attempt, left for the next run
	Requests   int64 `json:"requests"`
	Pruned     int64 `json:"pruned"` // keywords and vectors deleted because their work is gone
}

// work is an eligible work that needs something.
type work struct {
	book    enrich.Book
	text    string // its keywords' text, when they are current
	extract bool   // keywords missing or stale; else only the keyword vector is
}

type runner struct {
	pool     *pgxpool.Pool
	client   llm.Client
	llmModel string
	emb      embed.Embedder
	opts     Options
	start    time.Time
	todo     Stats // what the counting pass found

	extracted, embedded, failed, requests, pruned atomic.Int64

	mu       sync.Mutex
	failures []string
}

// Run indexes the eligible works whose keywords or keyword vector are missing or stale, most-rated first,
// then, unless Limit is set, deletes the keywords and vectors of works no longer in the catalogue. client is
// asked with llmModel, which is part of the keywords' hash. A first pass counts what to do; with DryRun, Run
// stops there. Keywords and vectors are stored as they come, so an interrupted run resumes where it stopped.
// A work whose extraction fails every attempt is logged and left for the next run.
func Run(ctx context.Context, pool *pgxpool.Pool, client llm.Client, llmModel string, emb embed.Embedder, opts Options) (Stats, error) {
	if opts.Concurrency <= 0 {
		opts.Concurrency = defaultConcurrency
	}

	if opts.PerRequest <= 0 {
		opts.PerRequest = defaultPerRequest
	}

	if opts.Attempts <= 0 {
		opts.Attempts = defaultAttempts
	}

	if opts.EmbedBatch <= 0 {
		opts.EmbedBatch = defaultEmbedBatch
	}

	if opts.ProgressInterval == 0 {
		opts.ProgressInterval = defaultProgressInterval
	}

	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	r := &runner{pool: pool, client: client, llmModel: llmModel, emb: emb, opts: opts, start: time.Now()}
	todo, err := r.scan(ctx, nil)
	r.todo = todo
	if err != nil {
		return r.stats(), err
	}

	r.opts.Logger.Info("to do", "considered", todo.Considered, "up_to_date", todo.UpToDate, "to_extract", todo.ToExtract,
		"to_embed", todo.ToEmbed)
	if opts.DryRun {
		return r.stats(), nil
	}

	if todo.ToExtract+todo.ToEmbed > 0 {
		if err := r.process(ctx); err != nil {
			r.report("stopped")
			return r.stats(), err
		}
	}

	if opts.Limit <= 0 {
		if err := r.prune(ctx); err != nil {
			return r.stats(), err
		}
	}

	r.report("done")
	r.listFailures()

	return r.stats(), nil
}

// scan reads the eligible works in order, up to the limit, and counts what they need. It passes those
// needing something to each, when set, and stops when each returns false.
func (r *runner) scan(ctx context.Context, each func(work) bool) (Stats, error) {
	var s Stats
	negRatings, last := math.MinInt32, ""
	for {
		size := pageSize
		if r.opts.Limit > 0 {
			size = min(size, r.opts.Limit-int(s.Considered))
		}

		if size <= 0 {
			return s, nil
		}

		rows, err := r.pool.Query(ctx, pendingSQL, negRatings, last, MinDescription, size)
		if err != nil {
			return s, fmt.Errorf("read works: %w", err)
		}

		// The page is read whole first, so that no query stays open while workers are busy.
		before := s.Considered
		var page []work
		for rows.Next() {
			var w work
			var ratings int
			var kwHash, vecHash string
			if err := rows.Scan(&w.book.ID, &w.book.Title, &w.book.Authors, &w.book.Description, &ratings,
				&kwHash, &w.text, &vecHash); err != nil {
				rows.Close()
				return s, fmt.Errorf("read works: %w", err)
			}

			s.Considered++
			negRatings, last = -ratings, w.book.ID
			w.book.Hash = enrich.ContentHash(r.llmModel, w.book)
			switch {
			case w.book.Hash != kwHash:
				w.extract = true
				s.ToExtract++
			case embed.KeywordHash(r.emb.Model(), w.text) != vecHash:
				s.ToEmbed++
			default:
				s.UpToDate++
				continue
			}

			page = append(page, w)
		}

		rows.Close()
		if err := rows.Err(); err != nil {
			return s, fmt.Errorf("read works: %w", err)
		}

		read := int(s.Considered - before)
		for _, w := range page {
			if each != nil && !each(w) {
				return s, nil
			}
		}

		if read < size {
			return s, nil
		}
	}
}

// process streams the works to do: groups of PerRequest works go to Concurrency extraction workers, and
// each work's keywords, once stored, go to the embedder, with the works whose vector alone is stale.
func (r *runner) process(ctx context.Context) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	groups := make(chan []enrich.Book)
	texts := make(chan embed.KeywordText, 4*r.opts.EmbedBatch)

	embedDone := make(chan struct{})
	go func() {
		defer close(embedDone)
		if err := r.embedAll(ctx, texts); err != nil {
			cancel(err)
		}
	}()

	var wg sync.WaitGroup
	for range r.opts.Concurrency {
		wg.Go(func() {
			for g := range groups {
				if err := r.extract(ctx, g, texts); err != nil {
					cancel(err)
				}
			}
		})
	}

	stopProgress := r.progress()
	sendGroup := func(g []enrich.Book) bool {
		select {
		case groups <- g:
			return true
		case <-ctx.Done():
			return false
		}
	}

	var group []enrich.Book
	_, scanErr := r.scan(ctx, func(w work) bool {
		if !w.extract {
			select {
			case texts <- embed.KeywordText{ID: w.book.ID, Text: w.text}:
				return true
			case <-ctx.Done():
				return false
			}
		}

		if group = append(group, w.book); len(group) < r.opts.PerRequest {
			return true
		}

		g := group
		group = nil

		return sendGroup(g)
	})
	if len(group) > 0 && scanErr == nil {
		sendGroup(group)
	}

	close(groups)
	wg.Wait()
	close(texts)
	<-embedDone
	stopProgress()

	if err := context.Cause(ctx); err != nil {
		return err
	}

	return scanErr
}

// extract extracts a group's keywords, stores them, and passes their texts to the embedder.
func (r *runner) extract(ctx context.Context, group []enrich.Book, texts chan<- embed.KeywordText) error {
	res := enrich.ExtractGroup(ctx, r.client, group, r.opts.Attempts)
	r.requests.Add(int64(res.Requests))

	// Keep what was extracted, even when ctx was cancelled.
	if err := enrich.Save(context.WithoutCancel(ctx), r.pool, r.llmModel, res.Done); err != nil {
		return err
	}

	r.extracted.Add(int64(len(res.Done)))
	if ctx.Err() == nil {
		for _, f := range res.Failed {
			r.failed.Add(1)
			r.opts.Logger.Warn("extraction failed, left for the next run", "work", f.Book.ID, "err", f.Err)
			r.mu.Lock()
			r.failures = append(r.failures, f.Book.ID)
			r.mu.Unlock()
		}
	}

	for _, e := range res.Done {
		select {
		case texts <- embed.KeywordText{ID: e.WorkID, Text: e.Keywords.Normalize().Text()}:
		case <-ctx.Done():
			// The keywords are stored; the next run embeds them.
			return nil
		}
	}

	if failed, extracted := r.failed.Load(), r.extracted.Load(); failed >= minFailures && failed > extracted {
		return fmt.Errorf("%w: %d failed, %d extracted", ErrTooManyFailures, failed, extracted)
	}

	return nil
}

// embedAll embeds the texts it receives, EmbedBatch at a time, or what it has after embedWait without a
// full batch, until texts is closed.
func (r *runner) embedAll(ctx context.Context, texts <-chan embed.KeywordText) error {
	var batch []embed.KeywordText
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}

		// Finish the batch even when the run is stopping: its keywords are stored already.
		if err := embed.StoreKeywordVectors(context.WithoutCancel(ctx), r.pool, r.emb, batch); err != nil {
			return err
		}

		r.embedded.Add(int64(len(batch)))
		batch = nil

		return nil
	}

	timer := time.NewTimer(embedWait)
	defer timer.Stop()
	for {
		select {
		case t, ok := <-texts:
			if !ok {
				return flush()
			}

			if batch = append(batch, t); len(batch) < r.opts.EmbedBatch {
				continue
			}
		case <-timer.C:
		}

		if err := flush(); err != nil {
			return err
		}

		timer.Reset(embedWait)
	}
}

// prune deletes the keywords and keyword vectors of works no longer in the catalogue.
func (r *runner) prune(ctx context.Context) error {
	keywords, err := enrich.Prune(ctx, r.pool)
	if err != nil {
		return err
	}

	vectors, err := embed.PruneKeywordVectors(ctx, r.pool)
	r.pruned.Store(keywords + vectors)

	return err
}

func (r *runner) stats() Stats {
	s := r.todo
	s.Extracted, s.Embedded, s.Failed = r.extracted.Load(), r.embedded.Load(), r.failed.Load()
	s.Requests, s.Pruned = r.requests.Load(), r.pruned.Load()

	return s
}

// progress logs progress every ProgressInterval until the returned function is called.
func (r *runner) progress() func() {
	if r.opts.ProgressInterval < 0 {
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		t := time.NewTicker(r.opts.ProgressInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				r.report("indexing")
			case <-done:
				return
			}
		}
	}()

	return func() { close(done) }
}

// report logs the counts, the rates per minute, the time left at this pace, and the LLM client's pacing when
// it has some (llm.Paced).
func (r *runner) report(msg string) {
	if r.opts.ProgressInterval < 0 {
		return
	}

	s := r.stats()
	minutes := time.Since(r.start).Minutes()
	attrs := []any{"extracted", s.Extracted, "to_extract", s.ToExtract, "embedded", s.Embedded,
		"to_embed", s.ToExtract + s.ToEmbed, "failed", s.Failed, "requests", s.Requests, "pruned", s.Pruned,
		"extracted_per_minute", fmt.Sprintf("%.0f", float64(s.Extracted)/minutes),
		"embedded_per_minute", fmt.Sprintf("%.0f", float64(s.Embedded)/minutes)}
	if left := eta(s, minutes); left >= time.Minute {
		attrs = append(attrs, "eta", left.Round(time.Minute))
	} else if left > 0 {
		attrs = append(attrs, "eta", left.Round(time.Second))
	}

	if p, ok := r.client.(interface{ Stats() llm.PacedStats }); ok {
		ps := p.Stats()
		attrs = append(attrs, "llm_concurrency", ps.Concurrency, "rate_limited", ps.RateLimited, "timed_out", ps.TimedOut,
			"restarts", ps.Restarts)
	}

	r.opts.Logger.Info(msg, attrs...)
}

// eta is the time left at the pace so far: the slower of extraction and embedding.
func eta(s Stats, minutes float64) time.Duration {
	left := 0.0
	if done := s.Extracted + s.Failed; done > 0 {
		left = float64(s.ToExtract-done) / (float64(done) / minutes)
	}

	if s.Embedded > 0 {
		left = max(left, float64(s.ToExtract+s.ToEmbed-s.Failed-s.Embedded)/(float64(s.Embedded)/minutes))
	}

	return time.Duration(left * float64(time.Minute))
}

// listFailures logs the works whose extraction failed in this run.
func (r *runner) listFailures() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.failures) == 0 {
		return
	}

	r.opts.Logger.Warn("works without keywords after this run; the next run tries them again", "count", len(r.failures),
		"works", r.failures[:min(len(r.failures), maxListedFailures)])
}
