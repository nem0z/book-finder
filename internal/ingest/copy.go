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

// copyStream COPYs rows built from a dump stream. toRow runs on the calling goroutine and may
// return nil to skip an item.
func copyStream[T any](ctx context.Context, pool *pgxpool.Pool, table string, cols []string, s *oldump.Stream[T], toRow func(T) []any) (int64, error) {
	src := &rowSource{next: func() ([]any, bool) {
		for v := range s.C {
			if row := toRow(v); row != nil {
				return row, true
			}
		}

		return nil, false
	}}
	n, err := pool.CopyFrom(ctx, pgx.Identifier{table}, cols, src)
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
