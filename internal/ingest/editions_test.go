package ingest

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nem0z/book-finder/internal/oldump"
)

func TestLoadEditions(t *testing.T) {
	dir := t.TempDir()
	l := newLoader(nil, Options{Sources: Sources{Editions: editionsFixture(t, dir), Redirects: redirectsFixture(t, dir)},
		Workers: 3, ProgressInterval: -1})
	l.works = map[string]*workAgg{"OL1W": {hasDescription: true}, "OL2W": {}, "OL4W": {}, "OL6W": {}}
	loadThrough(t, l, l.loadRedirects, l.loadEditions)

	if l.stats.Editions != 6 || l.stats.DecodeErrors != 0 {
		t.Errorf("stats: %+v", l.stats)
	}

	w := l.works["OL1W"]
	if w.english != 2 || w.earliestYear != 2018 || w.originalLanguage() != "nor" {
		t.Errorf("OL1W: english %d, earliest %d, language %q", w.english, w.earliestYear, w.originalLanguage())
	}

	// OL1M (pages, ISBN-13, cover) beats OL2M (pages only). The work has its own description, so
	// OL2M's is not kept.
	if b := w.best; b.pages != 480 || b.year != 2018 || b.isbn13 != "9780000000001" || b.coverID != 7 || w.description != "" {
		t.Errorf("OL1W best edition: %+v, description %q", b, w.description)
	}

	// Editions arrive in no fixed order, so the order and the first spelling seen ("Fiction." or
	// "fiction") vary.
	genres := slices.Clone(w.genres)
	for i, g := range genres {
		genres[i] = strings.ToLower(g)
	}

	if slices.Sort(genres); !slices.Equal(genres, []string{"fiction", "thriller"}) {
		t.Errorf("OL1W genres: %v", w.genres)
	}

	if w := l.works["OL2W"]; w.english != 0 || w.originalLanguage() != "nor" {
		t.Errorf("OL2W: english %d, language %q", w.english, w.originalLanguage())
	}

	w = l.works["OL4W"]
	if w.english != 1 || w.earliestYear != 1950 || w.originalLanguage() != "fre" ||
		w.description != "A French novel, translated." || w.best.year != 1990 {
		t.Errorf("OL4W: english %d, earliest %d, language %q, description %q, best %+v", w.english, w.earliestYear,
			w.originalLanguage(), w.description, w.best)
	}

	if w := l.works["OL6W"]; w.english != 1 || w.best.pages != 0 {
		t.Errorf("OL6W: english %d, pages %d (3 pages is implausible)", w.english, w.best.pages)
	}
}

func TestFirstISBN13(t *testing.T) {
	if got := firstISBN13([]string{"123", "0-14-303943-X", "978-0-14-303943-3"}); got != "9780143039433" {
		t.Errorf("got %q", got)
	}

	if got := firstISBN13(nil); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestCandidateWork(t *testing.T) {
	l := newLoader(nil, Options{})
	l.works = map[string]*workAgg{"OL1W": {}}
	if err := l.redirects.Add("/works/OL9W", "/works/OL1W"); err != nil {
		t.Fatal(err)
	}

	for raw, want := range map[string]error{
		`{"works": [{"key": "/works/OL9W"}]}`:          nil, // redirected to a candidate
		`{"works": [{"key": "/works/OL2W"}]}`:          errNotCandidate,
		`{"works": [{"key": "/works/OL4294967296W"}]}`: errNotCandidate, // invalid key
		`{"key": "/books/OL1M"}`:                       errNoWorkKey,
	} {
		id, err := l.candidateWork([]byte(raw))
		if !errors.Is(err, want) || (want == nil) != (id == "OL1W") {
			t.Errorf("%s: got %q, %v, want %v", raw, id, err, want)
		}

		if err != nil && !errors.Is(err, oldump.ErrSkip) {
			t.Errorf("%s: %v should be a skip", raw, err)
		}
	}
}
