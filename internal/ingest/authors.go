package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/nem0z/book-finder/internal/oldump"
)

var errNoName = fmt.Errorf("%w: author has no name", oldump.ErrSkip)

type authorRow struct {
	id, name, birth, death, wikidata string
}

// neededAuthor reports whether an author line's key (second column) is an author of a candidate
// work. It reads the key without splitting the whole line, so it is cheap enough for the filter.
// Author records are keyed by their current ID, so no redirect is followed.
func (l *loader) neededAuthor(line []byte) bool {
	rest := line[len(typeAuthor):]
	end := bytes.IndexByte(rest, '\t')
	if end < 0 {
		return false
	}

	_, ok := l.authorsNeeded[oldump.ShortKey(string(rest[:end]))]

	return ok
}

// decodeAuthor runs on decode workers.
func decodeAuthor(line []byte) (authorRow, error) {
	_, key, data, err := splitRecord(line)
	if err != nil {
		return authorRow{}, err
	}

	var a oldump.Author
	if err := json.Unmarshal(data, &a); err != nil {
		return authorRow{}, fmt.Errorf("author %s: %w", key, err)
	}

	name := oldump.Clean(a.Name)
	if name == "" {
		return authorRow{}, errNoName
	}

	return authorRow{
		id:       oldump.ShortKey(string(key)),
		name:     name,
		birth:    oldump.Clean(a.BirthDate),
		death:    oldump.Clean(a.DeathDate),
		wikidata: oldump.Clean(a.WikidataID()),
	}, nil
}

// loadAuthors COPYs the authors referenced by candidate works into the authors table. Authors of
// works dropped by finalize are deleted there.
func (l *loader) loadAuthors(ctx context.Context) error {
	filter := func(line []byte) bool {
		return bytes.HasPrefix(line, typeAuthor) && l.neededAuthor(line)
	}

	s := oldump.NewStream(ctx, l.opts.Sources.Authors, l.streamOpts("authors"), filter, decodeAuthor)
	seen := map[string]bool{}
	cols := []string{"id", "name", "birth_date", "death_date", "wikidata_id"}
	n, err := copyStream(ctx, l.pool, "authors", cols, s, func(a authorRow) []any {
		if seen[a.id] {
			return nil
		}

		seen[a.id] = true

		return []any{a.id, a.name, nullStr(a.birth), nullStr(a.death), nullStr(a.wikidata)}
	})
	l.countFailures("authors", s.Failed(), s.FirstFailure())
	l.stats.Authors = n
	l.log.Info("authors loaded", "authors", n, "referenced", len(l.authorsNeeded))

	return err
}
