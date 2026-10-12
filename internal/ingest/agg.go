package ingest

import "strings"

const (
	maxGenres = 10
	minPages  = 10
	maxPages  = 5000

	// Completeness weights: an edition with a description beats any edition without one,
	// then page count, ISBN-13 and cover break ties in that order.
	descriptionWeight = 8
	pagesWeight       = 4
	isbnWeight        = 2
	coverWeight       = 1
)

// workAgg accumulates everything learned about a candidate work from its editions, ratings and
// reading log. It is only touched by the single consumer goroutine.
type workAgg struct {
	hasDescription bool

	english        int32
	best           bestEdition
	genres         []string
	earliestYear   int16
	earliestLangYr int16
	earliestLang   string
	translatedFrom map[string]int32

	ratingSum        int32
	ratingCount      int32
	wantToRead       int32
	currentlyReading int32
	alreadyRead      int32
}

// editionFacts is what the loader keeps from one edition.
type editionFacts struct {
	english        bool
	year           int
	firstLanguage  string
	translatedFrom []string
	pages          int
	isbn13         string
	genres         []string
	description    string
	coverID        int64
}

// bestEdition is the most complete English edition seen so far; it fills in what the work lacks.
type bestEdition struct {
	found       bool
	score       int
	year        int16
	pages       int16
	isbn13      string
	description string // kept only when the work has no description of its own
	coverID     int64
}

// validPages returns the page count, or 0 when it is missing or implausible.
func (e editionFacts) validPages() int {
	if e.pages < minPages || e.pages > maxPages {
		return 0
	}

	return e.pages
}

// completeness ranks an English edition: description first, then page count, ISBN-13 and cover.
func (e editionFacts) completeness() int {
	score := 0
	if e.description != "" {
		score += descriptionWeight
	}

	if e.validPages() > 0 {
		score += pagesWeight
	}

	if e.isbn13 != "" {
		score += isbnWeight
	}

	if e.coverID > 0 {
		score += coverWeight
	}

	return score
}

func (a *workAgg) addEdition(e editionFacts) {
	a.trackEarliest(e)
	a.countTranslation(e)
	if !e.english {
		return
	}

	a.english++
	for _, g := range e.genres {
		a.addGenre(g)
	}

	a.considerBest(e)
}

// trackEarliest records the earliest edition year and the language of the earliest dated edition.
func (a *workAgg) trackEarliest(e editionFacts) {
	if e.year == 0 {
		return
	}

	year := int16(e.year)
	if a.earliestYear == 0 || year < a.earliestYear {
		a.earliestYear = year
	}

	if e.firstLanguage != "" && (a.earliestLangYr == 0 || year < a.earliestLangYr) {
		a.earliestLangYr = year
		a.earliestLang = e.firstLanguage
	}
}

// countTranslation tallies translated_from codes across all editions: a German edition
// translated from English is evidence too.
func (a *workAgg) countTranslation(e editionFacts) {
	for _, l := range e.translatedFrom {
		if a.translatedFrom == nil {
			a.translatedFrom = map[string]int32{}
		}

		a.translatedFrom[l]++
	}
}

// considerBest keeps e if it is more complete than the current best English edition; equally
// complete editions are replaced by more recent ones.
func (a *workAgg) considerBest(e editionFacts) {
	score := e.completeness()
	year := int16(e.year)
	if a.best.found && (score < a.best.score || (score == a.best.score && year <= a.best.year)) {
		return
	}

	description := ""
	if !a.hasDescription {
		description = e.description
	}

	a.best = bestEdition{
		found:       true,
		score:       score,
		year:        year,
		pages:       int16(e.validPages()),
		isbn13:      e.isbn13,
		description: description,
		coverID:     e.coverID,
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
