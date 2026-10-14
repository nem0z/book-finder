package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nem0z/book-finder/internal/oldump"
)

const maxSubjects = 100

// workRow is one row of the stage_works table.
type workRow struct {
	id, title, subtitle, description          string
	year                                      int
	subjects, places, people, times, authorID []string
	coverID                                   int64
}

func (workRow) columns() []string {
	return []string{"id", "title", "subtitle", "description", "first_publish_year", "subjects",
		"subject_places", "subject_people", "subject_times", "cover_id", "author_ids"}
}

func (r workRow) values() []any {
	return []any{r.id, r.title, nullStr(r.subtitle), nullStr(r.description), nullInt(r.year),
		r.subjects, r.places, r.people, r.times, nullInt64(r.coverID), r.authorID}
}

func cleanList(in []string, limit int) []string {
	out := make([]string, 0, min(len(in), limit))
	for _, s := range in {
		if s = oldump.Clean(s); s != "" {
			out = append(out, s)
			if len(out) == limit {
				break
			}
		}
	}

	return out
}

var (
	errNoTitle    = fmt.Errorf("%w: work has no title", oldump.ErrSkip)
	errNotFiction = fmt.Errorf("%w: work is not fiction", oldump.ErrSkip)
)

// decodeWork runs on decode workers. It keeps fiction works with a title.
func (l *loader) decodeWork(line []byte) (workRow, error) {
	_, key, data, err := splitRecord(line)
	if err != nil {
		return workRow{}, err
	}

	var w oldump.Work
	if err := json.Unmarshal(data, &w); err != nil {
		return workRow{}, fmt.Errorf("work %s: %w", key, err)
	}

	title := oldump.Clean(w.Title)
	if title == "" {
		return workRow{}, errNoTitle
	}

	if !oldump.IsFiction(w.Subjects) {
		return workRow{}, errNotFiction
	}

	return l.newWorkRow(oldump.ShortKey(string(key)), title, &w), nil
}

// newWorkRow builds the staged row for a fiction work.
func (l *loader) newWorkRow(id, title string, w *oldump.Work) workRow {
	return workRow{
		id:          id,
		title:       title,
		subtitle:    oldump.Clean(w.Subtitle),
		description: oldump.Clean(string(w.Description)),
		year:        oldump.ParseYear(w.FirstPublishDate, l.maxYear),
		subjects:    cleanList(w.Subjects, maxSubjects),
		places:      cleanList(w.SubjectPlaces, maxSubjects),
		people:      cleanList(w.SubjectPeople, maxSubjects),
		times:       cleanList(w.SubjectTimes, maxSubjects),
		coverID:     oldump.FirstCover(w.Covers),
		authorID:    l.resolveAuthors(w.Authors),
	}
}

// resolveAuthors returns the short IDs of a work's authors in order, following redirects and
// dropping duplicates and keys that are invalid, loop or are not authors.
func (l *loader) resolveAuthors(authors []oldump.WorkAuthor) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, a := range authors {
		k, err := l.redirects.Resolve(a.Key)
		if err != nil || !strings.HasPrefix(k, oldump.AuthorsPath) {
			continue
		}

		id := oldump.ShortKey(k)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	return ids
}

// loadWorks streams fiction works into stage_works and registers them as candidates.
func (l *loader) loadWorks(ctx context.Context) error {
	filter := func(line []byte) bool {
		return bytes.HasPrefix(line, typeWork) && oldump.MentionsFiction(line)
	}

	s := oldump.NewStream(ctx, l.opts.Sources.Works, l.streamOpts("works"), filter, l.decodeWork)
	_, err := copyStream(ctx, l.pool, "stage_works", s, func(r workRow) bool {
		if _, dup := l.works[r.id]; dup {
			return false
		}

		l.works[r.id] = &workAgg{hasDescription: r.description != ""}
		for _, a := range r.authorID {
			l.authorsNeeded[a] = struct{}{}
		}

		return true
	})
	l.countFailures("works", s.Failed(), s.FirstFailure())
	l.stats.CandidateWorks = len(l.works)
	l.log.Info("fiction candidates", "works", len(l.works), "authors referenced", len(l.authorsNeeded))
	return err
}
