package shops

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	createStageSQL = `
DROP TABLE IF EXISTS %[1]s;
CREATE UNLOGGED TABLE %[1]s (
    isbn_13      text NOT NULL,
    title        text NOT NULL,
    author_names text[] NOT NULL,
    description  text,
    year         smallint,
    genres       text[] NOT NULL,
    source_url   text NOT NULL,
    cover_url    text,
    in_stock     boolean NOT NULL
)`

	// insertStagedSQL adds the staged books as works of source $1, one per ISBN-13 (the first staged wins).
	// search_tsv is weighted like Open Library works': title and authors A, genres B, description C.
	insertStagedSQL = `
INSERT INTO works (id, source, title, description, description_source, first_publish_year, isbn_13, genres,
                   author_names, source_url, cover_url, in_stock, search_tsv)
SELECT DISTINCT ON (isbn_13)
    $1 || ':' || isbn_13, $1, title, description, CASE WHEN description IS NOT NULL THEN 'edition' END, year,
    isbn_13, genres, author_names, source_url, cover_url, in_stock,
    setweight(to_tsvector('english', title), 'A') ||
    setweight(to_tsvector('simple', array_to_string(author_names, ' ')), 'A') ||
    setweight(to_tsvector('english', array_to_string(genres, ' ')), 'B') ||
    setweight(to_tsvector('english', left(coalesce(description, ''), 20000)), 'C')
FROM %s
ORDER BY isbn_13, ctid`
)

var (
	stageColumns = []string{"isbn_13", "title", "author_names", "description", "year", "genres", "source_url", "cover_url", "in_stock"}

	sourceRe = regexp.MustCompile(`^[a-z0-9]+$`)

	// ErrBusy: another run holds the shop's stage.
	ErrBusy = errors.New("another run is loading this shop")
	// ErrShrink: the staged books are too few to replace the shop's works (see Stage.MinShare).
	ErrShrink = errors.New("too few books staged to replace the shop's works")
	// ErrClosed: the stage was closed.
	ErrClosed = errors.New("stage closed")
)

// Stage collects a shop's books in an unlogged table, so a crawl can stream them in pages and the catalogue
// only changes once it is complete (Replace). It holds a connection with an advisory lock on the shop for its
// whole life, so two runs cannot use the same stage; Close discards the stage and releases the lock.
type Stage struct {
	// MinShare is the least share of the shop's current works that Replace accepts to stage (0.5 by default), so
	// a catalogue answer cut short cannot wipe most of a shop; 0 accepts anything.
	MinShare float64

	conn   *pgxpool.Conn
	source string
	table  string // stage_works_<source>
}

// Replaced counts the works of a source before and after Replace.
type Replaced struct {
	Deleted, Inserted int64
}

// NewStage locks the shop and creates an empty stage for its books, dropping any stage a crashed run left
// behind. It returns ErrBusy when another run holds the shop.
func NewStage(ctx context.Context, pool *pgxpool.Pool, source string) (*Stage, error) {
	if !sourceRe.MatchString(source) {
		return nil, fmt.Errorf("stage: bad source %q: lowercase letters and digits only", source)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	s := &Stage{MinShare: 0.5, conn: conn, source: source, table: "stage_works_" + source}
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, s.table).Scan(&locked); err != nil {
		conn.Release()
		return nil, fmt.Errorf("lock stage: %w", err)
	}

	if !locked {
		conn.Release()
		return nil, fmt.Errorf("stage %s: %w", source, ErrBusy)
	}

	if _, err := conn.Exec(ctx, fmt.Sprintf(createStageSQL, s.table)); err != nil {
		s.Close(context.WithoutCancel(ctx)) //nolint:errcheck // the create error matters more
		return nil, fmt.Errorf("create stage: %w", err)
	}

	return s, nil
}

// Add stages books, which must be of the stage's source.
func (s *Stage) Add(ctx context.Context, books []Book) error {
	if s.conn == nil {
		return ErrClosed
	}

	rows := make([][]any, len(books))
	for i, b := range books {
		if b.Source != s.source {
			return fmt.Errorf("stage %s: book %s is from %q", s.source, b.ISBN13, b.Source)
		}

		rows[i] = []any{b.ISBN13, b.Title, nonNil(b.Authors), nullString(b.Description), nullYear(b.Year),
			nonNil(b.Genres), b.URL, nullString(b.CoverURL), b.InStock}
	}

	if _, err := s.conn.CopyFrom(ctx, pgx.Identifier{s.table}, stageColumns, pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("stage books: %w", err)
	}

	return nil
}

// Replace swaps the source's works for the staged books in one transaction, then drops the stage and
// refreshes the planner statistics of works. Other sources' works are left alone. It returns ErrShrink, and
// changes nothing, when fewer books than MinShare of the shop's current works are staged.
func (s *Stage) Replace(ctx context.Context) (Replaced, error) {
	var r Replaced
	if s.conn == nil {
		return r, ErrClosed
	}

	err := pgx.BeginFunc(ctx, s.conn, func(tx pgx.Tx) error {
		var staged, current int64
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(DISTINCT isbn_13) FROM %s`, s.table)).Scan(&staged); err != nil {
			return fmt.Errorf("count staged books: %w", err)
		}

		if err := tx.QueryRow(ctx, `SELECT count(*) FROM works WHERE source = $1`, s.source).Scan(&current); err != nil {
			return fmt.Errorf("count %s works: %w", s.source, err)
		}

		if float64(staged) < s.MinShare*float64(current) {
			return fmt.Errorf("%w: %d staged for %d %s works", ErrShrink, staged, current, s.source)
		}

		tag, err := tx.Exec(ctx, `DELETE FROM works WHERE source = $1`, s.source)
		if err != nil {
			return fmt.Errorf("delete %s works: %w", s.source, err)
		}

		r.Deleted = tag.RowsAffected()
		if tag, err = tx.Exec(ctx, fmt.Sprintf(insertStagedSQL, s.table), s.source); err != nil {
			return fmt.Errorf("insert %s works: %w", s.source, err)
		}

		r.Inserted = tag.RowsAffected()
		_, err = tx.Exec(ctx, "DROP TABLE "+s.table)

		return err
	})
	if err != nil {
		return r, err
	}

	if _, err := s.conn.Exec(ctx, `ANALYZE works`); err != nil {
		return r, fmt.Errorf("analyze works: %w", err)
	}

	return r, nil
}

// Close drops the stage if Replace did not, releases the lock on the shop and the connection. The stage cannot be
// used afterwards. When the lock cannot be released (e.g. ctx is cancelled), it closes the connection instead: a
// session lock would outlive the connection's return to the pool and keep the shop busy.
func (s *Stage) Close(ctx context.Context) error {
	if s.conn == nil {
		return nil
	}

	conn := s.conn
	s.conn = nil
	defer conn.Release()

	_, err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+s.table)
	if _, uerr := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtext($1))`, s.table); uerr != nil {
		if cerr := conn.Conn().Close(context.WithoutCancel(ctx)); cerr != nil {
			uerr = errors.Join(uerr, cerr)
		}

		err = errors.Join(err, fmt.Errorf("unlock stage: %w", uerr))
	}

	return err
}
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

func nullYear(y int) *int16 {
	if y == 0 {
		return nil
	}

	v := int16(y)

	return &v
}
