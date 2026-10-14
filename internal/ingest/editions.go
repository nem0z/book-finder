package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nem0z/book-finder/internal/oldump"
)

const englishLanguage = "eng"

var (
	errNoWorkKey    = fmt.Errorf("%w: edition has no work key", oldump.ErrSkip)
	errNotCandidate = fmt.Errorf("%w: work is not a fiction candidate", oldump.ErrSkip)
)

// editionRow is what a decode worker extracts from an edition of a candidate work.
type editionRow struct {
	workID string
	facts  editionFacts
}

// candidateWork returns the short ID of the candidate work an edition belongs to. It returns
// errNoWorkKey when the edition has no work key, and errNotCandidate when the key is invalid or the
// work is not a candidate. It only reads the raw line (the first "/works/OL..." key in it), so it is
// cheap enough for the filter; the common errors are preallocated for the same reason.
func (l *loader) candidateWork(raw []byte) (string, error) {
	key, ok := oldump.EditionWorkKey(raw)
	if !ok {
		return "", errNoWorkKey
	}

	full, err := l.redirects.Resolve(key)
	if err != nil {
		return "", errors.Join(errNotCandidate, err)
	}

	id := oldump.ShortKey(full)
	if _, ok := l.works[id]; !ok {
		return "", errNotCandidate
	}

	return id, nil
}

// decodeEdition runs on decode workers. Lines reaching it already belong to a candidate work.
func (l *loader) decodeEdition(line []byte) (editionRow, error) {
	_, key, data, err := splitRecord(line)
	if err != nil {
		return editionRow{}, err
	}

	workID, err := l.candidateWork(data)
	if err != nil {
		return editionRow{}, err
	}

	var e oldump.Edition
	if err := json.Unmarshal(data, &e); err != nil {
		return editionRow{}, fmt.Errorf("edition %s: %w", key, err)
	}

	return editionRow{workID: workID, facts: l.editionFacts(&e)}, nil
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

	// Both are kept: workAgg.originalLanguage prefers translated_from and falls back to the language
	// of the earliest edition.
	for _, t := range e.TranslatedFrom {
		if code := oldump.ShortKey(string(t)); code != "" {
			f.translatedFrom = append(f.translatedFrom, code)
		}
	}

	for _, lang := range e.Languages {
		if code := oldump.ShortKey(string(lang)); code != "" {
			f.firstLanguage = code
			break
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
		if !bytes.HasPrefix(line, typeEdition) {
			return false
		}

		_, err := l.candidateWork(line)
		return err == nil
	}

	s := oldump.NewStream(ctx, l.opts.Sources.Editions, l.streamOpts("editions"), filter, l.decodeEdition)
	for r := range s.C {
		l.works[r.workID].addEdition(r.facts)
		l.stats.Editions++
	}

	err := s.Err()
	l.countFailures("editions", s.Failed(), s.FirstFailure())
	l.log.Info("editions read", "editions", l.stats.Editions)

	return err
}
