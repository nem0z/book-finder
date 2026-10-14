package ingest

import (
	"context"
	"strconv"
	"testing"

	"github.com/nem0z/book-finder/internal/oldump"
)

type numberRow struct{ n int }

func (numberRow) columns() []string { return []string{"n"} }
func (r numberRow) values() []any   { return []any{r.n} }

type textRow struct{ s string }

func (textRow) columns() []string { return []string{"n"} }
func (r textRow) values() []any   { return []any{r.s} }

func TestCopyStream(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS copy_stream_test; CREATE TABLE copy_stream_test (n int)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE copy_stream_test") }) //nolint:errcheck

	path := writeFile(t, t.TempDir(), "numbers.txt.gz", "1\n2\n3\nx\n4\n", true)
	s := oldump.NewStream(ctx, path, oldump.StreamOptions{Workers: 2},
		func([]byte) bool { return true },
		func(line []byte) (numberRow, error) { n, err := strconv.Atoi(string(line)); return numberRow{n}, err })
	n, err := copyStream(ctx, pool, "copy_stream_test", s, func(r numberRow) bool { return r.n != 2 })
	if err != nil {
		t.Fatal(err)
	}

	var sum int
	if err := pool.QueryRow(ctx, "SELECT sum(n) FROM copy_stream_test").Scan(&sum); err != nil {
		t.Fatal(err)
	}

	if n != 3 || sum != 8 || s.Failed() != 1 {
		t.Errorf("copied %d rows, sum %d, failed %d", n, sum, s.Failed())
	}
}

func TestCopyStreamBadTable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	path := writeFile(t, t.TempDir(), "numbers.txt", "1\n2\n", false)
	s := oldump.NewStream(ctx, path, oldump.StreamOptions{}, func([]byte) bool { return true },
		func(line []byte) (textRow, error) { return textRow{string(line)}, nil })
	if _, err := copyStream(ctx, pool, "no_such_table", s, func(textRow) bool { return true }); err == nil {
		t.Error("expected an error") // and the stream must be stopped without hanging
	}
}

func TestNullHelpers(t *testing.T) {
	if nullStr("") != nil || nullStr("a") != "a" || nullInt(0) != nil || nullInt(3) != 3 || nullInt64(0) != nil {
		t.Error("null helpers")
	}
}
