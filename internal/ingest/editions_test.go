package ingest

import (
	"slices"
	"testing"
)

func TestLoadEditions(t *testing.T) {
	dir := t.TempDir()
	l := newLoader(nil, Options{Sources: Sources{Editions: editionsFixture(t, dir), Redirects: redirectsFixture(t, dir)},
		Workers: 3, ProgressInterval: -1})
	l.works = map[string]*workAgg{"OL1W": {hasDescription: true}, "OL2W": {}, "OL4W": {}, "OL6W": {}}
	loadThrough(t, l, l.loadRedirects, l.loadEditions)

	if l.stats.Editions != 6 || l.stats.WorksWithEnglishEdition != 3 || l.decodeErrors.Load() != 0 {
		t.Errorf("stats: %+v, decode errors %d", l.stats, l.decodeErrors.Load())
	}

	w := l.works["OL1W"]
	if w.english != 2 || w.earliestYear != 2018 || w.originalLanguage() != "nor" {
		t.Errorf("OL1W: english %d, earliest %d, language %q", w.english, w.earliestYear, w.originalLanguage())
	}

	// OL2M has a description, so it beats OL1M; the work has its own description, so OL2M's is not kept.
	if b := w.best; b.pages != 512 || b.year != 2019 || b.description != "" || b.isbn13 != "" {
		t.Errorf("OL1W best edition: %+v", b)
	}

	if !slices.Equal(w.genres, []string{"Fiction", "Thriller"}) {
		t.Errorf("OL1W genres: %v", w.genres)
	}

	if w := l.works["OL2W"]; w.english != 0 || w.originalLanguage() != "nor" {
		t.Errorf("OL2W: english %d, language %q", w.english, w.originalLanguage())
	}

	w = l.works["OL4W"]
	if w.english != 1 || w.earliestYear != 1950 || w.originalLanguage() != "fre" ||
		w.best.description != "A French novel, translated." || w.best.year != 1990 {
		t.Errorf("OL4W: english %d, earliest %d, language %q, best %+v", w.english, w.earliestYear, w.originalLanguage(), w.best)
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
