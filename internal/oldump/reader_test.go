package oldump

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeGzip(t *testing.T, path string, content string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadLinesGzipAndLongLines(t *testing.T) {
	long := strings.Repeat("x", 3<<20) // longer than the 1 MiB read buffer
	path := filepath.Join(t.TempDir(), "dump.txt.gz")
	writeGzip(t, path, "a\r\n"+long+"\n\nb")

	var got []int
	var last Progress
	err := ReadLines(context.Background(), path, 0, func(p Progress) { last = p }, func(line []byte) (bool, error) {
		got = append(got, len(line))
		return len(line) == 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(got, []int{1, len(long), 1}) {
		t.Errorf("line lengths: %v", got)
	}

	if last.Lines != 3 || last.Kept != 2 || last.Percent() != 100 {
		t.Errorf("final progress: %+v", last)
	}
}

func TestStream(t *testing.T) {
	var b strings.Builder
	for i := range 1000 {
		b.WriteString("/type/work\t/works/OL")
		b.WriteString(strings.Repeat("1", i%5+1))
		b.WriteString("W\t1\t2020\t{}\n")
	}

	path := filepath.Join(t.TempDir(), "works.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	filter := func(line []byte) bool { return !bytes.Contains(line, []byte("OL1W")) }
	decode := func(line []byte) (int, error) {
		switch {
		case bytes.Contains(line, []byte("OL11W")):
			return 0, fmt.Errorf("%w: not wanted", ErrSkip)
		case bytes.Contains(line, []byte("OL111W")):
			return 0, errors.New("malformed")
		}

		return len(line), nil
	}
	s := NewStream(context.Background(), path, StreamOptions{Workers: 4}, filter, decode)
	n := 0
	for range s.C {
		n++
	}

	if err := s.Err(); err != nil {
		t.Fatal(err)
	}

	if n != 400 || s.Skipped() != 200 || s.Failed() != 200 || s.FirstFailure() == nil {
		t.Errorf("got %d items, %d skipped, %d failed (%v)", n, s.Skipped(), s.Failed(), s.FirstFailure())
	}
}

func TestStreamMissingFile(t *testing.T) {
	s := NewStream(context.Background(), filepath.Join(t.TempDir(), "missing.txt.gz"), StreamOptions{},
		func([]byte) bool { return true }, func(l []byte) ([]byte, error) { return l, nil })
	for range s.C {
	}

	if s.Err() == nil {
		t.Error("expected an error for a missing file")
	}
}

func TestStreamStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "works.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("line\n", 100000)), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewStream(context.Background(), path, StreamOptions{Workers: 2},
		func([]byte) bool { return true }, func(l []byte) ([]byte, error) { return l, nil })
	<-s.C
	s.Stop() // must not deadlock
}
