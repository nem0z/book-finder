package ingest

import (
	"bytes"
	"context"
	"encoding/json"
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

	// Redirect chains longer than this are treated as loops.
	maxRedirectHops = 10

	prepareSQL = `
TRUNCATE works, work_authors, authors;
DROP TABLE IF EXISTS stage_works, stage_work_stats;
`
)

var (
	typeRedirect  = []byte("/type/redirect\t")
	worksPrefix   = []byte("/works/")
	authorsPrefix = []byte("/authors/")
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
	Redirects int `json:"redirects"`
}

type loader struct {
	pool    *pgxpool.Pool
	opts    Options
	log     *slog.Logger
	maxYear int

	redirects map[string]string // full key -> full key

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
		pool:      pool,
		opts:      opts,
		log:       opts.Logger,
		maxYear:   time.Now().Year() + 1,
		redirects: map[string]string{},
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

// resolve follows Open Library redirects (merged records) for a full key such as "/works/OL1W".
func (l *loader) resolve(key string) string {
	for range maxRedirectHops {
		next, ok := l.redirects[key]
		if !ok || next == key {
			return key
		}

		key = next
	}

	return key
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

// loadRedirects reads merged work and author IDs from the optional redirects dump.
func (l *loader) loadRedirects(ctx context.Context) error {
	path := l.opts.Sources.Redirects
	if path == "" {
		l.log.Info("no redirects dump; merged records will not be resolved")
		return nil
	}

	err := oldump.ReadLines(ctx, path, l.progressInterval(), l.progress("redirects"), l.addRedirect)
	l.stats.Redirects = len(l.redirects)

	return err
}

// addRedirect records one work or author redirect line; other redirects are ignored.
func (l *loader) addRedirect(line []byte) (bool, error) {
	if !bytes.HasPrefix(line, typeRedirect) {
		return false, nil
	}

	_, key, data, ok := oldump.SplitRecord(line)
	if !ok || !(bytes.HasPrefix(key, worksPrefix) || bytes.HasPrefix(key, authorsPrefix)) {
		return false, nil
	}

	var r struct {
		Location string `json:"location"`
	}

	if json.Unmarshal(data, &r) != nil || r.Location == "" {
		return false, nil
	}

	l.redirects[string(key)] = r.Location

	return true, nil
}
