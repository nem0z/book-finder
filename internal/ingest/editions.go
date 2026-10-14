package ingest

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/nem0z/book-finder/internal/oldump"
)

const englishLanguage = "eng"

// editionRow is what a decode worker extracts from an edition of a candidate work.
type editionRow struct {
	workID string
	facts  editionFacts
}

// candidateWork returns the short ID of the candidate work an edition belongs to, or "" when the
// edition has no work reference, the reference is invalid, or the work is not a candidate. It only
// reads the raw line (the first "/works/OL..." key in it), so it is cheap enough for the filter.
func (l *loader) candidateWork(raw []byte) string {
	key, ok := oldump.EditionWorkKey(raw)
	if !ok {
		return ""
	}

	full, err := l.redirects.Resolve(key)
	if err != nil {
		return ""
	}

	id := oldump.ShortKey(full)
	if _, ok := l.works[id]; !ok {
		return ""
	}

	return id
}

// decodeEdition runs on decode workers. Lines reaching it already belong to a candidate work.
func (l *loader) decodeEdition(line []byte) (editionRow, bool) {
	_, _, data, ok := oldump.SplitRecord(line)
	if !ok {
		return editionRow{}, false
	}

	workID := l.candidateWork(data)
	if workID == "" {
		return editionRow{}, false
	}

	var e oldump.Edition
	if err := json.Unmarshal(data, &e); err != nil {
		l.decodeErrors.Add(1)
		return editionRow{}, false
	}

	return editionRow{workID: workID, facts: l.editionFacts(&e)}, true
}

func (l *loader) editionFacts(e *oldump.Edition) editionFacts {
	f := editionFacts{
		english:     e.HasLanguage(englishLanguage),
		year:        oldump.ParseYear(e.PublishDate, l.maxYear),
		pages:       int(e.NumberOfPages),
		isbn13:      firstISBN13(e.ISBN13),
		genres:      cleanList(e.Genres, maxGenres),
		description: oldump.Clean(string(e.Description)),
		coverID:     oldump.FirstCover(e.Covers),
	}

	for _, lang := range e.Languages {
		if code := oldump.ShortKey(string(lang)); code != "" {
			f.firstLanguage = code
			break
		}
	}

	for _, t := range e.TranslatedFrom {
		if code := oldump.ShortKey(string(t)); code != "" {
			f.translatedFrom = append(f.translatedFrom, code)
		}
	}

	return f
}

// firstISBN13 returns the first value that normalizes to a 13-digit ISBN, or "".
func firstISBN13(values []string) string {
	for _, v := range values {
		if isbn := oldump.NormalizeISBN(v); len(isbn) == oldump.ISBN13Length {
			return isbn
		}
	}

	return ""
}

// loadEditions reads the editions of candidate works and adds each one to its work's aggregate in
// memory: English edition count, most complete English edition, earliest year, original language and
// genres. Nothing is written to the database; editions are not stored.
func (l *loader) loadEditions(ctx context.Context) error {
	filter := func(line []byte) bool {
		return bytes.HasPrefix(line, typeEdition) && l.candidateWork(line) != ""
	}

	s := oldump.NewStream(ctx, l.opts.Sources.Editions, l.streamOpts("editions"), filter, l.decodeEdition)
	for r := range s.C {
		l.works[r.workID].addEdition(r.facts)
		l.stats.Editions++
	}

	for _, a := range l.works {
		if a.english > 0 {
			l.stats.WorksWithEnglishEdition++
		}
	}

	l.log.Info("editions read", "editions", l.stats.Editions, "works with an English edition", l.stats.WorksWithEnglishEdition)

	return s.Err()
}
