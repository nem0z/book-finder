package ingest

import (
	"slices"
	"testing"
)

func TestBestEdition(t *testing.T) {
	a := &workAgg{}
	a.addEdition(editionFacts{english: true, year: 2001, pages: 300, isbn13: "9780000000001", coverID: 5})
	a.addEdition(editionFacts{english: true, year: 1999, pages: 250, description: "Old blurb."})
	a.addEdition(editionFacts{english: true, year: 2010, pages: 9, description: "New blurb."}) // invalid pages
	a.addEdition(editionFacts{english: false, year: 2020, pages: 500, description: "Ein Klappentext.", isbn13: "x", coverID: 9})

	if a.english != 3 {
		t.Errorf("english editions: %d", a.english)
	}

	// 2001 (pages + isbn + cover = 7) beats 1999 (pages = 4) and 2010 (nothing valid = 0).
	if a.best.year != 2001 || a.best.pages != 300 || a.best.isbn13 != "9780000000001" || a.best.coverID != 5 {
		t.Errorf("best: %+v", a.best)
	}

	// The longest English description, whichever edition it comes from; the German one is ignored.
	if a.description != "Old blurb." {
		t.Errorf("description: %q", a.description)
	}
}

func TestBestEditionTieGoesToMostRecent(t *testing.T) {
	a := &workAgg{}
	a.addEdition(editionFacts{english: true, year: 2005, pages: 200})
	a.addEdition(editionFacts{english: true, year: 2015, pages: 220})
	a.addEdition(editionFacts{english: true, year: 2010, pages: 240})
	if a.best.year != 2015 || a.best.pages != 220 {
		t.Errorf("best: %+v", a.best)
	}
}

func TestEditionDescriptionOnlyWhenWorkHasNone(t *testing.T) {
	a := &workAgg{hasDescription: true}
	a.addEdition(editionFacts{english: true, description: "edition text"})
	if a.description != "" {
		t.Errorf("edition description should not be kept when the work has one: %q", a.description)
	}
}

func TestGenresAndEarliest(t *testing.T) {
	a := &workAgg{}
	a.addEdition(editionFacts{english: false, year: 2017, firstLanguage: "nob"})
	a.addEdition(editionFacts{english: true, year: 2018, firstLanguage: "eng", translatedFrom: []string{"nor"},
		genres: []string{"Fiction.", "Thriller"}})
	a.addEdition(editionFacts{english: true, year: 2019, genres: []string{"fiction", " "}})

	if !slices.Equal(a.genres, []string{"Fiction", "Thriller"}) {
		t.Errorf("genres: %v", a.genres)
	}

	if a.earliestYear != 2017 || a.originalLanguage() != "nor" {
		t.Errorf("earliest %d, language %q", a.earliestYear, a.originalLanguage())
	}
}

func TestOriginalLanguageFallback(t *testing.T) {
	a := &workAgg{}
	a.addEdition(editionFacts{english: true, year: 1990, firstLanguage: "eng"})
	a.addEdition(editionFacts{english: false, year: 1960, firstLanguage: "fre"})
	a.addEdition(editionFacts{english: false, firstLanguage: "ger"}) // undated: ignored
	a.addEdition(editionFacts{english: false, year: 1950, firstLanguage: "und", translatedFrom: []string{"mul"}})
	if got := a.originalLanguage(); got != "fre" {
		t.Errorf("got %q", got)
	}

	if (&workAgg{}).originalLanguage() != "" {
		t.Error("empty agg should have no language")
	}
}
