package ingest

import (
	"context"
	"strconv"
	"testing"

	"github.com/nem0z/book-finder/internal/oldump"
)

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
		func(line []byte) (int, bool) { n, err := strconv.Atoi(string(line)); return n, err == nil })
	n, err := copyStream(ctx, pool, "copy_stream_test", []string{"n"}, s, func(v int) []any {
		if v == 2 {
			return nil
		}

		return []any{v}
	})
	if err != nil {
		t.Fatal(err)
	}

	var sum int
	if err := pool.QueryRow(ctx, "SELECT sum(n) FROM copy_stream_test").Scan(&sum); err != nil {
		t.Fatal(err)
	}

	if n != 3 || sum != 8 || s.Skipped() != 1 {
		t.Errorf("copied %d rows, sum %d, skipped %d", n, sum, s.Skipped())
	}
}

func TestCopyStreamBadTable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	path := writeFile(t, t.TempDir(), "numbers.txt", "1\n2\n", false)
	s := oldump.NewStream(ctx, path, oldump.StreamOptions{}, func([]byte) bool { return true },
		func(line []byte) (string, bool) { return string(line), true })
	if _, err := copyStream(ctx, pool, "no_such_table", []string{"n"}, s, func(v string) []any { return []any{v} }); err == nil {
		t.Error("expected an error") // and the stream must be stopped without hanging
	}
}

func TestNullHelpers(t *testing.T) {
	if nullStr("") != nil || nullStr("a") != "a" || nullInt(0) != nil || nullInt(3) != 3 || nullInt64(0) != nil {
		t.Error("null helpers")
	}
}
