package embed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultBatchSize        = 32
	defaultPageSize         = 1000
	defaultProgressInterval = 15 * time.Second

	// DefaultMinDescription is the shortest description, in bytes, worth a vector by default:
	// shorter ones say too little about the story for semantic search.
	DefaultMinDescription = 300
)

// eligibleSQL selects works worth a vector, most-rated first: a description of at least $1 bytes
// without links or HTML (those are usually series lists or markup, not a summary). Pages are read
// after the ($2, $3) position in that order.
const eligibleSQL = `
SELECT w.id, w.title, coalesce(w.subtitle, ''), w.author_names, w.genres, w.subjects, w.description,
       w.rating_count, coalesce(e.content_hash, '')
FROM works w
LEFT JOIN work_embeddings e ON e.work_id = w.id
WHERE length(w.description) >= $1
  AND w.description !~ '(https?://|\[[0-9]+\]:|<[a-zA-Z]+[ >/])'
  AND (-w.rating_count, w.id) > ($2, $3)
ORDER BY -w.rating_count, w.id
LIMIT $4`

const upsertSQL = `
INSERT INTO work_embeddings (work_id, model, content_hash, embedding)
SELECT t.id, $2, t.hash, t.vec::halfvec
FROM unnest($1::text[], $3::text[], $4::text[]) AS t(id, hash, vec)
ON CONFLICT (work_id) DO UPDATE
SET model = EXCLUDED.model, content_hash = EXCLUDED.content_hash, embedding = EXCLUDED.embedding,
    created_at = now()`

const pruneSQL = `DELETE FROM work_embeddings e WHERE NOT EXISTS (SELECT 1 FROM works w WHERE w.id = e.work_id)`

var ErrEmptyCatalogue = errors.New("no works in the catalogue; load it first")

// Embedder turns texts into vectors of Dims floats. *Ollama implements it; tests use a fake.
type Embedder interface {
	Model() string
	EmbedDocuments(ctx context.Context, docs []Doc) ([][]float32, error)
}

type Options struct {
	// Limit > 0 only considers the Limit most-rated eligible works.
	Limit int
	// MinDescription is the minimum description length in bytes; 0 means DefaultMinDescription.
	MinDescription   int
	BatchSize        int           // texts per embedder call; default 32
	ProgressInterval time.Duration // default 15s; negative disables progress logs
	Logger           *slog.Logger
}

type Stats struct {
	Considered int64 `json:"considered"` // eligible works read
	Embedded   int64 `json:"embedded"`   // vectors computed and stored
	UpToDate   int64 `json:"up_to_date"` // already embedded from the same model and text
	Pruned     int64 `json:"pruned"`     // vectors deleted because their work is gone
}

type job struct {
	id, hash string
	doc      Doc
}

// cursor is the position after the last work read, in eligibleSQL order.
type cursor struct {
	negRatings int
	id         string
}

// page is what readPage read: the jobs to run, the position of the last work and how many works.
type page struct {
	jobs []job
	last cursor
	read int
}

type runner struct {
	pool  *pgxpool.Pool
	emb   Embedder
	opts  Options
	start time.Time
	last  time.Time
	stats Stats
}

// Run embeds eligible works whose vector is missing or was computed from another model or text, most
// rated first, then deletes vectors of works no longer in the catalogue. Finished batches are kept, so
// an interrupted run resumes where it stopped.
func Run(ctx context.Context, pool *pgxpool.Pool, emb Embedder, opts Options) (Stats, error) {
	if opts.MinDescription <= 0 {
		opts.MinDescription = DefaultMinDescription
	}

	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultBatchSize
	}

	if opts.ProgressInterval == 0 {
		opts.ProgressInterval = defaultProgressInterval
	}

	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	// Pruning against an empty catalogue (e.g. during a reload) would delete every vector.
	var works int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM works").Scan(&works); err != nil {
		return Stats{}, err
	}

	if works == 0 {
		return Stats{}, ErrEmptyCatalogue
	}

	r := &runner{pool: pool, emb: emb, opts: opts, start: time.Now(), last: time.Now()}
	if err := r.embedEligible(ctx); err != nil {
		return r.stats, err
	}

	tag, err := pool.Exec(ctx, pruneSQL)
	if err != nil {
		return r.stats, fmt.Errorf("prune: %w", err)
	}

	r.stats.Pruned = tag.RowsAffected()
	r.report()

	return r.stats, nil
}

func (r *runner) embedEligible(ctx context.Context) error {
	last := cursor{negRatings: math.MinInt32}
	for size := r.pageSize(); size > 0; size = r.pageSize() {
		p, err := r.readPage(ctx, last, size)
		if err != nil {
			return err
		}

		if err := r.embedJobs(ctx, p.jobs); err != nil {
			return err
		}

		if p.read < size {
			return nil
		}

		last = p.last
	}

	return nil
}

// pageSize returns how many works to read next: a full page, fewer near the limit, 0 once it is reached.
func (r *runner) pageSize() int {
	if r.opts.Limit <= 0 {
		return defaultPageSize
	}

	return min(defaultPageSize, r.opts.Limit-int(r.stats.Considered))
}

// readPage reads up to size eligible works after last.
func (r *runner) readPage(ctx context.Context, last cursor, size int) (page, error) {
	p := page{last: last}
	rows, err := r.pool.Query(ctx, eligibleSQL, r.opts.MinDescription, last.negRatings, last.id, size)
	if err != nil {
		return p, fmt.Errorf("read works: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		if err := r.readRow(rows, &p); err != nil {
			return p, err
		}
	}

	return p, rows.Err()
}

// readRow adds the current row to p: it moves p's position, and adds a job when the work's vector is
// missing or was computed from another model or text.
func (r *runner) readRow(rows pgx.Rows, p *page) error {
	var b Book
	var id, hash string
	var ratings int
	if err := rows.Scan(&id, &b.Title, &b.Subtitle, &b.Authors, &b.Genres, &b.Subjects, &b.Description, &ratings, &hash); err != nil {
		return fmt.Errorf("read works: %w", err)
	}

	p.read++
	p.last = cursor{negRatings: -ratings, id: id}
	r.stats.Considered++

	doc := Document(b)
	want := ContentHash(r.emb.Model(), doc)
	if want == hash {
		r.stats.UpToDate++
		return nil
	}

	p.jobs = append(p.jobs, job{id: id, hash: want, doc: doc})

	return nil
}

// embedJobs embeds and stores jobs in batches, reporting progress between batches.
func (r *runner) embedJobs(ctx context.Context, jobs []job) error {
	for batch := range slices.Chunk(jobs, r.opts.BatchSize) {
		if err := r.embedBatch(ctx, batch); err != nil {
			return err
		}

		r.maybeReport()
	}

	return nil
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
		return fmt.Errorf("%w: %d vectors for %d texts", ErrBadResponse, len(vecs), len(batch))
	}

	ids := make([]string, len(batch))
	hashes := make([]string, len(batch))
	literals := make([]string, len(batch))
	for i, j := range batch {
		ids[i], hashes[i], literals[i] = j.id, j.hash, VectorLiteral(vecs[i])
	}

	if _, err := r.pool.Exec(ctx, upsertSQL, ids, r.emb.Model(), hashes, literals); err != nil {
		return fmt.Errorf("store vectors: %w", err)
	}

	r.stats.Embedded += int64(len(batch))

	return nil
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

	rate := float64(r.stats.Embedded) / time.Since(r.start).Seconds()
	r.opts.Logger.Info("embedding", "considered", r.stats.Considered, "embedded", r.stats.Embedded,
		"up_to_date", r.stats.UpToDate, "pruned", r.stats.Pruned, "per_second", fmt.Sprintf("%.1f", rate))
}

// VectorLiteral formats v as a pgvector text literal ("[0.1,0.2,...]"), cast to halfvec in SQL.
func VectorLiteral(v []float32) string {
	b := make([]byte, 0, len(v)*10)
	b = append(b, '[')
	for i, f := range v {
		if i > 0 {
			b = append(b, ',')
		}

		b = strconv.AppendFloat(b, float64(f), 'g', -1, 32)
	}

	return string(append(b, ']'))
}
