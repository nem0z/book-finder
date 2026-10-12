package ingest

import (
	"slices"
	"strings"
)

const (
	maxPageSamples = 32
	maxGenres      = 10
	minPages       = 10
	maxPages       = 5000
)

// workAgg accumulates everything learned about a candidate work from editions,
// ratings and reading logs. It is only touched by the single consumer goroutine.
type workAgg struct {
	hasDescription bool

	editions         int32
	english          int32
	pages            []int16
	genres           []string
	editionDesc      string
	firstSentence    string
	coverID          int64
	earliestYear     int16
	earliestLangYr   int16
	earliestLang     string
	translatedFrom   map[string]int32
	ratingSum        int32
	ratingCount      int32
	wantToRead       int32
	currentlyReading int32
	alreadyRead      int32
}

type editionFacts struct {
	english        bool
	year           int
	firstLanguage  string
	translatedFrom []string
	pages          int
	genres         []string
	description    string
	firstSentence  string
	coverID        int64
}

func (a *workAgg) addEdition(e editionFacts) {
	a.editions++
	if e.year > 0 && (a.earliestYear == 0 || int16(e.year) < a.earliestYear) {
		a.earliestYear = int16(e.year)
	}
	if e.year > 0 && e.firstLanguage != "" && (a.earliestLangYr == 0 || int16(e.year) < a.earliestLangYr) {
		a.earliestLangYr = int16(e.year)
		a.earliestLang = e.firstLanguage
	}
	for _, l := range e.translatedFrom {
		if a.translatedFrom == nil {
			a.translatedFrom = map[string]int32{}
		}
		a.translatedFrom[l]++
	}
	if !e.english {
		return
	}

	a.english++
	if e.pages >= minPages && e.pages <= maxPages && len(a.pages) < maxPageSamples {
		a.pages = append(a.pages, int16(e.pages))
	}
	for _, g := range e.genres {
		a.addGenre(g)
	}
	if !a.hasDescription && len(e.description) > len(a.editionDesc) {
		a.editionDesc = e.description
	}
	if a.firstSentence == "" {
		a.firstSentence = e.firstSentence
	}
	if a.coverID == 0 {
		a.coverID = e.coverID
	}
}

func (a *workAgg) addGenre(g string) {
	g = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(g), "."))
	if g == "" || len(a.genres) >= maxGenres {
		return
	}
	for _, have := range a.genres {
		if strings.EqualFold(have, g) {
			return
		}
	}
	a.genres = append(a.genres, g)
}

// medianPages returns the median English page count, or 0 when unknown.
func (a *workAgg) medianPages() int {
	if len(a.pages) == 0 {
		return 0
	}
	p := slices.Clone(a.pages)
	slices.Sort(p)
	n := len(p)
	if n%2 == 1 {
		return int(p[n/2])
	}
	return (int(p[n/2-1]) + int(p[n/2])) / 2
}

// originalLanguage prefers the most frequent translated_from code (ties broken
// alphabetically), else the language of the earliest dated edition.
func (a *workAgg) originalLanguage() string {
	best, bestN := "", int32(0)
	for l, n := range a.translatedFrom {
		if n > bestN || (n == bestN && l < best) {
			best, bestN = l, n
		}
	}
	if best != "" {
		return best
	}
	return a.earliestLang
}
