package embed

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Options struct {
	BatchSize int // texts per embedder call (default 32)
	Workers   int // concurrent embedder calls (default 2)
	// Also embed works without a description (title, authors and topics only). Off by default:
	// those vectors carry little meaning and add noise to semantic search.
	IncludeUndescribed bool
	// Limit > 0 embeds at most that many works, most-read first. Useful for a quick trial.
	Limit            int
	PageSize         int           // works read per database page (default 2000)
	ProgressInterval time.Duration // default 15s; negative disables
	Logger           *slog.Logger
}

type Stats struct {
	Considered int64 `json:"considered"`
	Embedded   int64 `json:"embedded"`
	UpToDate   int64 `json:"up_to_date"`
	Pruned     int64 `json:"pruned"`
}

type job struct {
	id   string
	doc  Doc
	hash string
}

const selectColumns = `w.id, w.title, coalesce(w.subtitle, ''), w.author_names, w.genres, w.subjects,
	coalesce(w.description, ''), coalesce(e.content_hash, '')`

const upsertSQL = `
INSERT INTO work_embeddings (work_id, model, content_hash, embedding, created_at)
SELECT t.id, $2::text, t.hash, t.emb::halfvec, now()
FROM unnest($1::text[], $3::text[], $4::text[]) AS t(id, hash, emb)
ON CONFLICT (work_id) DO UPDATE
SET model = EXCLUDED.model, content_hash = EXCLUDED.content_hash,
    embedding = EXCLUDED.embedding, created_at = EXCLUDED.created_at`

// Run embeds every work whose embedding is missing or stale (text or model changed), then removes
// embeddings of works that no longer exist. It is resumable: interrupted runs keep finished batches.
func Run(ctx context.Context, pool *pgxpool.Pool, emb Embedder, opts Options) (Stats, error) {
	if opts.BatchSize <= 0 {
		opts.BatchSize = 32
	}
	if opts.Workers <= 0 {
		opts.Workers = 2
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 2000
	}
	if opts.ProgressInterval == 0 {
		opts.ProgressInterval = 15 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	r := &runner{pool: pool, emb: emb, opts: opts, log: opts.Logger, start: time.Now()}

	var total int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM works WHERE $1 OR description IS NOT NULL",
		opts.IncludeUndescribed).Scan(&total); err != nil {
		return r.stats(), err
	}
	r.total = total
	r.log.Info("embedding", "model", emb.Model(), "works", total, "batch", opts.BatchSize, "workers", opts.Workers)

	var err error
	if opts.Limit > 0 {
		err = r.page(ctx, `SELECT `+selectColumns+`
			FROM works w LEFT JOIN work_embeddings e ON e.work_id = w.id
			WHERE ($1 OR w.description IS NOT NULL)
			ORDER BY w.want_to_read + w.currently_reading + w.already_read DESC, w.id
			LIMIT $2`, opts.IncludeUndescribed, opts.Limit)
	} else {
		err = r.all(ctx)
	}
	if err != nil {
		return r.stats(), err
	}
	r.report()

	// Guard against wiping embeddings while the catalogue is empty (e.g. during a reload).
	if total > 0 {
		tag, err := pool.Exec(ctx, `DELETE FROM work_embeddings e
			WHERE NOT EXISTS (SELECT 1 FROM works w WHERE w.id = e.work_id)`)
		if err != nil {
			return r.stats(), err
		}
		r.pruned = tag.RowsAffected()
	}
	return r.stats(), nil
}

type runner struct {
	pool  *pgxpool.Pool
	emb   Embedder
	opts  Options
	log   *slog.Logger
	start time.Time
	total int64

	considered, embedded, upToDate atomic.Int64
	pruned                         int64
	lastReport                     time.Time
}

func (r *runner) stats() Stats {
	return Stats{Considered: r.considered.Load(), Embedded: r.embedded.Load(), UpToDate: r.upToDate.Load(), Pruned: r.pruned}
}

func (r *runner) report() {
	if r.opts.ProgressInterval < 0 {
		return
	}
	n := r.embedded.Load()
	rate := float64(n) / time.Since(r.start).Seconds()
	attrs := []any{"considered", r.considered.Load(), "of", r.total, "embedded", n,
		"up_to_date", r.upToDate.Load(), "per_second", fmt.Sprintf("%.1f", rate)}
	if remaining := r.total - r.considered.Load(); rate > 0 && remaining > 0 {
		attrs = append(attrs, "eta", (time.Duration(float64(remaining)/rate) * time.Second).Round(time.Minute))
	}
	r.log.Info("progress", attrs...)
}

// all walks the catalogue in id order, one page at a time, so no transaction stays open for long.
func (r *runner) all(ctx context.Context) error {
	last := ""
	for {
		n, lastID, err := r.pageCount(ctx, `SELECT `+selectColumns+`
			FROM works w LEFT JOIN work_embeddings e ON e.work_id = w.id
			WHERE ($1 OR w.description IS NOT NULL) AND w.id > $2
			ORDER BY w.id
			LIMIT $3`, r.opts.IncludeUndescribed, last, r.opts.PageSize)
		if err != nil {
			return err
		}
		if n < r.opts.PageSize {
			return nil
		}
		last = lastID
	}
}

func (r *runner) page(ctx context.Context, sql string, args ...any) error {
	_, _, err := r.pageCount(ctx, sql, args...)
	return err
}

// pageCount reads one page of works, embeds the stale ones and returns the row count and last id.
func (r *runner) pageCount(ctx context.Context, sql string, args ...any) (int, string, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return 0, "", err
	}
	var jobs []job
	n, lastID := 0, ""
	for rows.Next() {
		var b Book
		var hash string
		if err := rows.Scan(&b.ID, &b.Title, &b.Subtitle, &b.Authors, &b.Genres, &b.Subjects, &b.Description, &hash); err != nil {
			rows.Close()
			return n, lastID, err
		}
		n++
		lastID = b.ID
		doc := Document(b)
		want := ContentHash(r.emb.Model(), doc)
		if hash == want {
			r.upToDate.Add(1)
			continue
		}
		jobs = append(jobs, job{id: b.ID, doc: doc, hash: want})
	}
	if err := rows.Err(); err != nil {
		return n, lastID, err
	}
	err = r.embedJobs(ctx, jobs)
	r.considered.Add(int64(n))
	if r.opts.ProgressInterval >= 0 && time.Since(r.lastReport) >= r.opts.ProgressInterval {
		r.report()
		r.lastReport = time.Now()
	}
	return n, lastID, err
}

func (r *runner) embedJobs(parent context.Context, jobs []job) error {
	if len(jobs) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	batches := make(chan []job)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for range r.opts.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range batches {
				if err := r.embedBatch(ctx, b); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
send:
	for i := 0; i < len(jobs); i += r.opts.BatchSize {
		select {
		case batches <- jobs[i:min(i+r.opts.BatchSize, len(jobs))]:
		case <-ctx.Done():
			break send
		}
	}
	close(batches)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return parent.Err()
}

func (r *runner) embedBatch(ctx context.Context, batch []job) error {
	docs := make([]Doc, len(batch))
	for i, j := range batch {
		docs[i] = j.doc
	}
	vecs, err := r.emb.EmbedDocuments(ctx, docs)
	if err != nil {
		return err
	}
	if len(vecs) != len(batch) {
		return fmt.Errorf("embedder returned %d vectors for %d texts", len(vecs), len(batch))
	}
	ids := make([]string, len(batch))
	hashes := make([]string, len(batch))
	lits := make([]string, len(batch))
	for i, j := range batch {
		if len(vecs[i]) != Dims {
			return fmt.Errorf("embedder returned %d dimensions, schema expects %d", len(vecs[i]), Dims)
		}
		ids[i], hashes[i], lits[i] = j.id, j.hash, VectorLiteral(vecs[i])
	}
	if _, err := r.pool.Exec(ctx, upsertSQL, ids, r.emb.Model(), hashes, lits); err != nil {
		return fmt.Errorf("store embeddings: %w", err)
	}
	r.embedded.Add(int64(len(batch)))
	return nil
}
