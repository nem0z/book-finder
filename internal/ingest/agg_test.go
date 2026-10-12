package ingest

import (
	"slices"
	"testing"
)

func TestWorkAggEditions(t *testing.T) {
	a := &workAgg{}
	a.addEdition(editionFacts{english: false, year: 2017, firstLanguage: "nob", pages: 500})
	a.addEdition(editionFacts{english: true, year: 2018, firstLanguage: "eng", translatedFrom: []string{"nor"},
		pages: 400, genres: []string{"Fiction.", "Thriller"}, description: "short", firstSentence: "It began.", coverID: 7})
	a.addEdition(editionFacts{english: true, year: 2019, firstLanguage: "eng", pages: 300,
		genres: []string{"fiction", " "}, description: "a longer description"})
	a.addEdition(editionFacts{english: true, pages: 9}) // below minPages: ignored

	if a.editions != 4 || a.english != 3 {
		t.Errorf("counts: %d editions, %d english", a.editions, a.english)
	}
	if got := a.medianPages(); got != 350 {
		t.Errorf("median pages: %d", got)
	}
	if got := a.originalLanguage(); got != "nor" {
		t.Errorf("original language: %q", got)
	}
	if !slices.Equal(a.genres, []string{"Fiction", "Thriller"}) {
		t.Errorf("genres: %v", a.genres)
	}
	if a.editionDesc != "a longer description" || a.firstSentence != "It began." || a.coverID != 7 {
		t.Errorf("text/cover: %q %q %d", a.editionDesc, a.firstSentence, a.coverID)
	}
	if a.earliestYear != 2017 {
		t.Errorf("earliest year: %d", a.earliestYear)
	}
}

func TestWorkAggKeepsWorkDescription(t *testing.T) {
	a := &workAgg{hasDescription: true}
	a.addEdition(editionFacts{english: true, description: "edition text"})
	if a.editionDesc != "" {
		t.Errorf("edition description should be ignored when the work has one")
	}
}

func TestOriginalLanguageFallback(t *testing.T) {
	a := &workAgg{}
	a.addEdition(editionFacts{english: true, year: 1990, firstLanguage: "eng"})
	a.addEdition(editionFacts{english: false, year: 1960, firstLanguage: "fre"})
	a.addEdition(editionFacts{english: false, firstLanguage: "ger"}) // undated: ignored
	if got := a.originalLanguage(); got != "fre" {
		t.Errorf("got %q", got)
	}
	if (&workAgg{}).originalLanguage() != "" {
		t.Error("empty agg should have no language")
	}
}
