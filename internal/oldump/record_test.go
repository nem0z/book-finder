package oldump

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTextBlock(t *testing.T) {
	for in, want := range map[string]string{
		`"plain"`:                              "plain",
		`{"type": "/type/text", "value": "v"}`: "v",
		`null`:                                 "",
		`42`:                                   "",
	} {
		var tb TextBlock
		if err := json.Unmarshal([]byte(in), &tb); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if string(tb) != want {
			t.Errorf("%s: got %q want %q", in, tb, want)
		}
	}
}

func TestWorkDecodeVariants(t *testing.T) {
	data := `{"key": "/works/OL1W", "title": "T",
		"authors": [
			{"author": {"key": "/authors/OL1A"}, "type": {"key": "/type/author_role"}},
			{"key": "/authors/OL2A"},
			"/authors/OL3A",
			{"author": "/authors/OL4A"}
		],
		"subjects": ["Fiction", 3, "", "Thrillers"],
		"covers": [-1, 123, "456"],
		"description": {"type": "/type/text", "value": "D"}}`
	var w Work
	if err := json.Unmarshal([]byte(data), &w); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, a := range w.Authors {
		keys = append(keys, a.Key)
	}
	if want := []string{"/authors/OL1A", "/authors/OL2A", "/authors/OL3A", "/authors/OL4A"}; !slices.Equal(keys, want) {
		t.Errorf("authors: %v", keys)
	}
	if !slices.Equal(w.Subjects, []string{"Fiction", "Thrillers"}) {
		t.Errorf("subjects: %v", w.Subjects)
	}
	if FirstCover(w.Covers) != 123 {
		t.Errorf("cover: %d", FirstCover(w.Covers))
	}
	if w.Description != "D" {
		t.Errorf("description: %q", w.Description)
	}
}

func TestEditionHelpers(t *testing.T) {
	data := []byte(`{"key": "/books/OL1M", "works": [{"key": "/works/OL9W"}], "languages": [{"key": "/languages/eng"}], "number_of_pages": "320"}`)
	var e Edition
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}
	if !e.HasLanguage("eng") || e.HasLanguage("fre") {
		t.Error("HasLanguage")
	}
	if e.NumberOfPages != 320 {
		t.Errorf("pages: %d", e.NumberOfPages)
	}
	if k, ok := EditionWorkKey(data); !ok || k != "/works/OL9W" {
		t.Errorf("EditionWorkKey: %q %v", k, ok)
	}
	if _, ok := EditionWorkKey([]byte(`{"key": "/books/OL1M"}`)); ok {
		t.Error("EditionWorkKey should fail without works")
	}
}

func TestParseYear(t *testing.T) {
	for in, want := range map[string]int{
		"1922": 1922, "March 3, 1922": 1922, "c1922": 1922, "19--": 0, "": 0,
		"12345": 0, "3000": 0, "May 2017, reprinted 2019": 2017,
	} {
		if got := ParseYear(in, 2027); got != want {
			t.Errorf("%q: got %d want %d", in, got, want)
		}
	}
}

func TestIsFiction(t *testing.T) {
	cases := []struct {
		subjects []string
		want     bool
	}{
		{[]string{"Fiction"}, true},
		{[]string{"Detective and mystery fiction"}, true},
		{[]string{"Juvenile nonfiction"}, false},
		{[]string{"Non-fiction"}, false},
		{[]string{"Cooking"}, false},
		{[]string{"Juvenile nonfiction", "Fiction, general"}, true},
		{nil, false},
	}
	for _, c := range cases {
		if got := IsFiction(c.subjects); got != c.want {
			t.Errorf("%v: got %v", c.subjects, got)
		}
	}
}

func TestNormalizeISBNAndClean(t *testing.T) {
	if got := NormalizeISBN("978-0-14-303943-3"); got != "9780143039433" {
		t.Errorf("isbn13: %q", got)
	}
	if got := NormalizeISBN("0 14 303943 x"); got != "014303943X" {
		t.Errorf("isbn10: %q", got)
	}
	if got := NormalizeISBN("123"); got != "" {
		t.Errorf("short: %q", got)
	}
	if got := Clean("  a\x00b \n"); got != "ab" {
		t.Errorf("clean: %q", got)
	}
}

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
	err := ReadLines(context.Background(), path, 0, nil, func(line []byte) (bool, error) {
		got = append(got, len(line))
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int{1, len(long), 1}) {
		t.Errorf("line lengths: %v", got)
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
	decode := func(line []byte) (int, bool) { return len(line), !bytes.Contains(line, []byte("OL11W")) }
	s := NewStream(context.Background(), path, StreamOptions{Workers: 4}, filter, decode)
	n := 0
	for range s.C {
		n++
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 600 || s.Skipped() != 200 {
		t.Errorf("got %d items, %d skipped", n, s.Skipped())
	}
}

func TestStreamStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "works.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("line\n", 100000)), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStream(context.Background(), path, StreamOptions{Workers: 2},
		func([]byte) bool { return true }, func(l []byte) ([]byte, bool) { return l, true })
	<-s.C
	s.Stop() // must not deadlock
}
