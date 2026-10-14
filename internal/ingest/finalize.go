package ingest

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nem0z/book-finder/internal/db"
)

// finalizeSQL builds the catalogue from the staging tables in one implicit transaction. Only works
// with a row in stage_work_stats are kept: those with an English edition and a description.
const finalizeSQL = `
INSERT INTO works (
    id, title, subtitle, description, description_source, first_publish_year, original_language,
    page_count, isbn_13, subjects, subject_places, subject_people, subject_times, genres,
    author_names, cover_id, search_tsv
)
SELECT
    w.id, w.title, w.subtitle,
    coalesce(w.description, s.edition_description),
    CASE WHEN w.description IS NOT NULL THEN 'work' ELSE 'edition' END,
    coalesce(w.first_publish_year, s.earliest_year),
    s.original_language, s.page_count, s.isbn_13,
    w.subjects, w.subject_places, w.subject_people, w.subject_times, s.genres,
    an.names, coalesce(w.cover_id, s.cover_id),
    setweight(to_tsvector('english', w.title || ' ' || coalesce(w.subtitle, '')), 'A') ||
    setweight(to_tsvector('simple', array_to_string(an.names, ' ')), 'A') ||
    setweight(to_tsvector('english', left(array_to_string(
        w.subjects || s.genres || w.subject_places || w.subject_people || w.subject_times, ' '), 20000)), 'B') ||
    setweight(to_tsvector('english', left(coalesce(w.description, s.edition_description), 20000)), 'C')
FROM stage_works w
JOIN stage_work_stats s ON s.id = w.id
CROSS JOIN LATERAL (
    SELECT coalesce(array_agg(a.name ORDER BY u.ord), '{}') AS names
    FROM unnest(w.author_ids) WITH ORDINALITY AS u(author_id, ord)
    JOIN authors a ON a.id = u.author_id
) an;

INSERT INTO work_authors (work_id, author_id, position)
SELECT w.id, u.author_id, (u.ord - 1)::smallint
FROM stage_works w
JOIN works k ON k.id = w.id
CROSS JOIN LATERAL unnest(w.author_ids) WITH ORDINALITY AS u(author_id, ord)
JOIN authors a ON a.id = u.author_id;

DELETE FROM authors a WHERE NOT EXISTS (SELECT 1 FROM work_authors wa WHERE wa.author_id = a.id);

DROP TABLE stage_works, stage_work_stats;
`

// finalize writes the editions information kept in memory to stage_work_stats, builds works,
// work_authors and the final authors from the staging tables, then rebuilds the indexes.
func (l *loader) finalize(ctx context.Context) error {
	n, err := l.copyWorkStats(ctx)
	if err != nil {
		return fmt.Errorf("copy work stats: %w", err)
	}

	l.log.Info("works kept", "works", n)
	if _, err := l.pool.Exec(ctx, finalizeSQL); err != nil {
		return fmt.Errorf("build catalogue: %w", err)
	}

	l.log.Info("building indexes")
	if _, err := l.pool.Exec(ctx, db.IndexesSQL()); err != nil {
		return fmt.Errorf("create indexes: %w", err)
	}

	if _, err := l.pool.Exec(ctx, "ANALYZE works, work_authors, authors"); err != nil {
		return fmt.Errorf("analyze: %w", err)
	}

	return nil
}

// copyWorkStats COPYs what the editions pass learned about each candidate that will be kept: one
// with an English edition and a description, its own or its best English edition's.
func (l *loader) copyWorkStats(ctx context.Context) (int64, error) {
	ids := make([]string, 0, len(l.works))
	for id, a := range l.works {
		if a.english > 0 && (a.hasDescription || a.best.description != "") {
			ids = append(ids, id)
		}
	}

	i := 0
	src := &rowSource{next: func() ([]any, bool) {
		if i == len(ids) {
			return nil, false
		}

		id := ids[i]
		a := l.works[id]
		i++

		genres := a.genres
		if genres == nil {
			genres = []string{}
		}

		return []any{id, nullStr(a.best.description), nullInt(int(a.earliestYear)), nullStr(a.originalLanguage()),
			nullInt(int(a.best.pages)), nullStr(a.best.isbn13), genres, nullInt64(a.best.coverID)}, true
	}}
	cols := []string{"id", "edition_description", "earliest_year", "original_language", "page_count",
		"isbn_13", "genres", "cover_id"}

	return l.pool.CopyFrom(ctx, pgx.Identifier{"stage_work_stats"}, cols, src)
}
