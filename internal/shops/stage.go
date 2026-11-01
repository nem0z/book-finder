package shops

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	stageTable = "stage_shop_works"

	createStageSQL = `
DROP TABLE IF EXISTS stage_shop_works;
CREATE UNLOGGED TABLE stage_shop_works (
    isbn_13      text NOT NULL,
    title        text NOT NULL,
    author_names text[] NOT NULL,
    description  text,
    year         smallint,
    genres       text[] NOT NULL,
    source_url   text NOT NULL,
    cover_url    text
)`

	dropStageSQL = `DROP TABLE IF EXISTS stage_shop_works`

	// insertStagedSQL adds the staged books as works of source $1, one per ISBN-13 (the first staged wins).
	// search_tsv is weighted like Open Library works': title and authors A, genres B, description C.
	insertStagedSQL = `
INSERT INTO works (id, source, title, description, description_source, first_publish_year, isbn_13, genres,
                   author_names, source_url, cover_url, search_tsv)
SELECT DISTINCT ON (isbn_13)
    $1 || ':' || isbn_13, $1, title, description, CASE WHEN description IS NOT NULL THEN 'edition' END, year,
    isbn_13, genres, author_names, source_url, cover_url,
    setweight(to_tsvector('english', title), 'A') ||
    setweight(to_tsvector('simple', array_to_string(author_names, ' ')), 'A') ||
    setweight(to_tsvector('english', array_to_string(genres, ' ')), 'B') ||
    setweight(to_tsvector('english', left(coalesce(description, ''), 20000)), 'C')
FROM stage_shop_works
ORDER BY isbn_13, ctid`
)

var stageColumns = []string{"isbn_13", "title", "author_names", "description", "year", "genres", "source_url", "cover_url"}

// Stage collects a shop's books in an unlogged table, so a crawl can stream them in pages and the catalogue
// only changes once it is complete (Replace). Drop discards it.
type Stage struct {
	pool   *pgxpool.Pool
	source string
}

// Replaced counts the works of a source before and after Replace.
type Replaced struct {
	Deleted, Inserted int64
}

// NewStage creates an empty stage for the books of source, dropping any stage a crashed run left behind.
func NewStage(ctx context.Context, pool *pgxpool.Pool, source string) (*Stage, error) {
	if _, err := pool.Exec(ctx, createStageSQL); err != nil {
		return nil, fmt.Errorf("create stage: %w", err)
	}

	return &Stage{pool: pool, source: source}, nil
}

// Add stages books, which must be of the stage's source.
func (s *Stage) Add(ctx context.Context, books []Book) error {
	rows := make([][]any, len(books))
	for i, b := range books {
		if b.Source != s.source {
			return fmt.Errorf("stage %s: book %s is from %q", s.source, b.ISBN13, b.Source)
		}

		rows[i] = []any{b.ISBN13, b.Title, nonNil(b.Authors), nullString(b.Description), nullYear(b.Year),
			nonNil(b.Genres), b.URL, nullString(b.CoverURL)}
	}

	if _, err := s.pool.CopyFrom(ctx, pgx.Identifier{stageTable}, stageColumns, pgx.CopyFromRows(rows)); err != nil {
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
		if tag, err = tx.Exec(ctx, insertStagedSQL, s.source); err != nil {
			return fmt.Errorf("insert %s works: %w", s.source, err)
		}

		r.Inserted = tag.RowsAffected()
		_, err = tx.Exec(ctx, dropStageSQL)

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
	_, err := s.pool.Exec(ctx, dropStageSQL)

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
