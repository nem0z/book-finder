package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/nem0z/book-finder/internal/oldump"
)

const maxSubjects = 100

type workRow struct {
	id, title, subtitle, description          string
	year                                      int
	subjects, places, people, times, authorID []string
	coverID                                   int64
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

// decodeWork runs on decode workers. It keeps fiction works with a title.
func (l *loader) decodeWork(line []byte) (workRow, bool) {
	_, key, data, ok := oldump.SplitRecord(line)
	if !ok {
		return workRow{}, false
	}

	var w oldump.Work
	if err := json.Unmarshal(data, &w); err != nil {
		l.decodeErrors.Add(1)
		return workRow{}, false
	}

	title := oldump.Clean(w.Title)
	if title == "" || !oldump.IsFiction(w.Subjects) {
		return workRow{}, false
	}

	return l.newWorkRow(oldump.ShortKey(string(key)), title, &w), true
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
	cols := []string{"id", "title", "subtitle", "description", "first_publish_year", "subjects",
		"subject_places", "subject_people", "subject_times", "cover_id", "author_ids"}
	_, err := copyStream(ctx, l.pool, "stage_works", cols, s, func(r workRow) []any {
		if _, dup := l.works[r.id]; dup {
			return nil
		}

		l.works[r.id] = &workAgg{hasDescription: r.description != ""}
		for _, a := range r.authorID {
			l.authorsNeeded[a] = struct{}{}
		}

		return []any{r.id, r.title, nullStr(r.subtitle), nullStr(r.description), nullInt(r.year),
			r.subjects, r.places, r.people, r.times, nullInt64(r.coverID), r.authorID}
	})
	l.stats.CandidateWorks = len(l.works)
	l.log.Info("fiction candidates", "works", len(l.works), "authors referenced", len(l.authorsNeeded))
	return err
}
