package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/oldump"
)

const (
	defaultProgressInterval = 15 * time.Second

	prepareSQL = `
TRUNCATE works, work_authors, authors;
DROP TABLE IF EXISTS stage_works, stage_work_stats;
CREATE UNLOGGED TABLE stage_works (
    id text, title text, subtitle text, description text, first_publish_year smallint,
    subjects text[], subject_places text[], subject_people text[], subject_times text[],
    cover_id bigint, author_ids text[]
);
`
)

// Sources are paths to dump files (.txt.gz or plain .txt).
type Sources struct {
	Works      string `json:"works"`
	Editions   string `json:"editions"`
	Authors    string `json:"authors"`
	Ratings    string `json:"ratings,omitempty"`
	ReadingLog string `json:"reading_log,omitempty"`
	Redirects  string `json:"redirects,omitempty"`
}

type Options struct {
	Sources          Sources
	Workers          int           // JSON decode goroutines; defaults to runtime.NumCPU()
	ProgressInterval time.Duration // defaults to 15s; negative disables progress logs
	Logger           *slog.Logger
}

type Stats struct {
	Redirects      redirectStats `json:"redirects"`
	CandidateWorks int           `json:"candidate_works"`
	Editions       int64         `json:"editions"`      // editions of candidate works
	Authors        int64         `json:"authors"`       // authors of candidate works
	DecodeErrors   int64         `json:"decode_errors"` // lines that could not be read, all passes
}

type loader struct {
	pool    *pgxpool.Pool
	opts    Options
	log     *slog.Logger
	maxYear int

	redirects     *oldump.Redirects
	works         map[string]*workAgg // short work ID -> aggregate
	authorsNeeded map[string]struct{} // short author IDs referenced by candidate works

	stats Stats
}

func newLoader(pool *pgxpool.Pool, opts Options) *loader {
	if opts.Workers <= 0 {
		opts.Workers = runtime.NumCPU()
	}

	if opts.ProgressInterval == 0 {
		opts.ProgressInterval = defaultProgressInterval
	}

	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	return &loader{
		pool:          pool,
		opts:          opts,
		log:           opts.Logger,
		maxYear:       time.Now().Year() + 1,
		redirects:     oldump.NewRedirects(),
		works:         map[string]*workAgg{},
		authorsNeeded: map[string]struct{}{},
	}
}

// Run rebuilds the catalogue from the dumps. The schema must already be migrated.
func Run(ctx context.Context, pool *pgxpool.Pool, opts Options) (Stats, error) {
	src := opts.Sources
	if src.Works == "" || src.Editions == "" || src.Authors == "" {
		return Stats{}, errors.New("works, editions and authors dumps are required")
	}

	l := newLoader(pool, opts)
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"prepare", l.prepare},
		{"redirects", l.loadRedirects},
		{"works", l.loadWorks},
		{"editions", l.loadEditions},
		{"authors", l.loadAuthors},
	}

	for _, s := range steps {
		t := time.Now()
		l.log.Info("step started", "step", s.name)
		if err := s.fn(ctx); err != nil {
			return l.stats, fmt.Errorf("%s: %w", s.name, err)
		}

		l.log.Info("step done", "step", s.name, "took", time.Since(t).Round(time.Millisecond))
	}

	return l.stats, nil
}

func (l *loader) streamOpts(step string) oldump.StreamOptions {
	return oldump.StreamOptions{Workers: l.opts.Workers, ProgressInterval: l.progressInterval(), OnProgress: l.progress(step)}
}

func (l *loader) progressInterval() time.Duration {
	if l.opts.ProgressInterval < 0 {
		return 0
	}

	return l.opts.ProgressInterval
}

func (l *loader) progress(step string) func(oldump.Progress) {
	return func(p oldump.Progress) {
		l.log.Info("reading", "step", step, "percent", fmt.Sprintf("%.1f", p.Percent()), "lines", p.Lines, "kept", p.Kept)
	}
}

// prepare drops secondary indexes (rebuilt in finalize), empties the catalogue and resets staging tables.
func (l *loader) prepare(ctx context.Context) error {
	var b strings.Builder
	for _, name := range db.IndexNames() {
		fmt.Fprintf(&b, "DROP INDEX IF EXISTS %s;\n", name)
	}

	b.WriteString(prepareSQL)
	if _, err := l.pool.Exec(ctx, b.String()); err != nil {
		return fmt.Errorf("reset catalogue: %w", err)
	}

	return nil
}

// countFailures adds a pass's unreadable lines to the stats and logs the first one.
func (l *loader) countFailures(step string, failed int64, first error) {
	if failed == 0 {
		return
	}

	l.stats.DecodeErrors += failed
	l.log.Warn("lines could not be read", "step", step, "count", failed, "first", first)
}

// splitRecord is oldump.SplitRecord with an error for malformed lines.
func splitRecord(line []byte) (typ, key, data []byte, err error) {
	typ, key, data, ok := oldump.SplitRecord(line)
	if !ok {
		return nil, nil, nil, fmt.Errorf("malformed line: %.80q", line)
	}

	return typ, key, data, nil
}
