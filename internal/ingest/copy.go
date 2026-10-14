package ingest

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/oldump"
)

// rowSource adapts a pull function to pgx.CopyFromSource.
type rowSource struct {
	next func() ([]any, bool)
	row  []any
}

// copyRow is a row type for COPY: it lists its columns and gives its values in the same order, so
// both are defined next to the struct's fields.
type copyRow interface {
	columns() []string
	values() []any
}

// copyStream COPYs the rows of a dump stream into table. keep runs on the calling goroutine, in
// stream order; rows it rejects are not written.
func copyStream[T copyRow](ctx context.Context, pool *pgxpool.Pool, table string, s *oldump.Stream[T], keep func(T) bool) (int64, error) {
	src := &rowSource{next: func() ([]any, bool) {
		for v := range s.C {
			if keep(v) {
				return v.values(), true
			}
		}

		return nil, false
	}}

	var zero T
	n, err := pool.CopyFrom(ctx, pgx.Identifier{table}, zero.columns(), src)
	if err != nil {
		s.Stop()
		return n, fmt.Errorf("copy %s: %w", table, err)
	}

	return n, s.Err()
}

func (s *rowSource) Next() bool {
	row, ok := s.next()
	s.row = row
	return ok
}

func (s *rowSource) Values() ([]any, error) { return s.row, nil }
func (s *rowSource) Err() error             { return nil }

// Null helpers map Go zero values to SQL NULL in COPY rows.

func nullStr(s string) any {
	if s == "" {
		return nil
	}

	return s
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}

	return v
}

func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}

	return v
}
