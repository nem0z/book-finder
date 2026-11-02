package shops

import (
	"context"
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
)

// Stage collects a shop's books in an unlogged table, so a crawl can stream them in pages and the catalogue
// only changes once it is complete (Replace). Drop discards it.
type Stage struct {
	pool   *pgxpool.Pool
	source string
	table  string // stage_works_<source>, so shops can be loaded at the same time
}

// Replaced counts the works of a source before and after Replace.
type Replaced struct {
	Deleted, Inserted int64
}

// NewStage creates an empty stage for the books of source, dropping any stage a crashed run left behind.
func NewStage(ctx context.Context, pool *pgxpool.Pool, source string) (*Stage, error) {
	if !sourceRe.MatchString(source) {
		return nil, fmt.Errorf("stage: bad source %q: lowercase letters and digits only", source)
	}

	s := &Stage{pool: pool, source: source, table: "stage_works_" + source}
	if _, err := pool.Exec(ctx, fmt.Sprintf(createStageSQL, s.table)); err != nil {
		return nil, fmt.Errorf("create stage: %w", err)
	}

	return s, nil
}

// Add stages books, which must be of the stage's source.
func (s *Stage) Add(ctx context.Context, books []Book) error {
	rows := make([][]any, len(books))
	for i, b := range books {
		if b.Source != s.source {
			return fmt.Errorf("stage %s: book %s is from %q", s.source, b.ISBN13, b.Source)
		}

		rows[i] = []any{b.ISBN13, b.Title, nonNil(b.Authors), nullString(b.Description), nullYear(b.Year),
			nonNil(b.Genres), b.URL, nullString(b.CoverURL), b.InStock}
	}

	if _, err := s.pool.CopyFrom(ctx, pgx.Identifier{s.table}, stageColumns, pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("stage books: %w", err)
	}

	return nil
}

// Replace swaps the source's works for the staged books in one transaction, then drops the stage and
// refreshes the planner statistics of works. Other sources' works are left alone.
func (s *Stage) Replace(ctx context.Context) (Replaced, error) {
	var r Replaced
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
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

	if _, err := s.pool.Exec(ctx, `ANALYZE works`); err != nil {
		return r, fmt.Errorf("analyze works: %w", err)
	}

	return r, nil
}

// Drop discards the stage. It does nothing once the stage was dropped or replaced.
func (s *Stage) Drop(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, "DROP TABLE IF EXISTS "+s.table)

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
