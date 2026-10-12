// Package ingest loads English-language fiction from the Open Library dumps into Postgres.
//
// It is a full rebuild: final tables are truncated, dumps are streamed into unlogged staging
// tables with COPY, then the final tables are filled in a few set-based statements and the
// secondary indexes are rebuilt.
//
// Passes: redirects -> works (fiction candidates) -> editions (English filter, pages, original
// language, fallback descriptions) -> authors -> ratings -> reading log -> finalize.
package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/oldump"
)

// Sources are paths to dump files (.txt.gz or plain .txt).
type Sources struct {
	Works      string `json:"works"`
	Editions   string `json:"editions"`
	Authors    string `json:"authors"`
	Ratings    string `json:"ratings,omitempty"`
	ReadingLog string `json:"reading_log,omitempty"`
	Redirects  string `json:"redirects,omitempty"`
}

type Options struct {
	Sources          Sources
	Workers          int           // JSON decode goroutines; defaults to runtime.NumCPU()
	ProgressInterval time.Duration // defaults to 15s; negative disables progress logs
	Logger           *slog.Logger
}

type Stats struct {
	Redirects            int   `json:"redirects"`
	CandidateWorks       int   `json:"candidate_works"`
	CandidateEditions    int64 `json:"candidate_editions"`
	EnglishEditions      int64 `json:"english_editions"`
	Ratings              int64 `json:"ratings"`
	ReadingLogEntries    int64 `json:"reading_log_entries"`
	DecodeErrors         int64 `json:"decode_errors"`
	Works                int64 `json:"works"`
	WorksWithDescription int64 `json:"works_with_description"`
	Editions             int64 `json:"editions"`
	Authors              int64 `json:"authors"`
	WorkAuthors          int64 `json:"work_authors"`
}

type loader struct {
	pool    *pgxpool.Pool
	opts    Options
	log     *slog.Logger
	maxYear int

	redirects     map[string]string   // full key -> full key
	works         map[string]*workAgg // short work ID -> aggregate
	authorsNeeded map[string]struct{} // short author IDs referenced by candidate works

	decodeErrors atomic.Int64
	stats        Stats
}

// Run rebuilds the catalogue from the dumps. The schema must already be migrated.
func Run(ctx context.Context, pool *pgxpool.Pool, opts Options) (Stats, error) {
	src := opts.Sources
	if src.Works == "" || src.Editions == "" || src.Authors == "" {
		return Stats{}, errors.New("works, editions and authors dumps are required")
	}
	if opts.Workers <= 0 {
		opts.Workers = runtime.NumCPU()
	}
	if opts.ProgressInterval == 0 {
		opts.ProgressInterval = 15 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	l := &loader{
		pool:          pool,
		opts:          opts,
		log:           opts.Logger,
		maxYear:       time.Now().Year() + 1,
		redirects:     map[string]string{},
		works:         map[string]*workAgg{},
		authorsNeeded: map[string]struct{}{},
	}

	started := time.Now()
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"prepare", l.prepare},
		{"redirects", l.loadRedirects},
		{"works", l.loadWorks},
		{"editions", l.loadEditions},
		{"authors", l.loadAuthors},
		{"ratings", l.loadRatings},
		{"reading log", l.loadReadingLog},
		{"work stats", l.copyWorkStats},
		{"finalize", l.finalize},
	}
	for _, s := range steps {
		t := time.Now()
		l.log.Info("step started", "step", s.name)
		if err := s.fn(ctx); err != nil {
			return l.stats, fmt.Errorf("%s: %w", s.name, err)
		}
		l.log.Info("step done", "step", s.name, "took", time.Since(t).Round(time.Millisecond))
	}
	l.stats.DecodeErrors = l.decodeErrors.Load()

	if err := l.recordRun(ctx, started); err != nil {
		return l.stats, fmt.Errorf("record run: %w", err)
	}
	return l.stats, nil
}

func (l *loader) streamOpts(step string) oldump.StreamOptions {
	return oldump.StreamOptions{Workers: l.opts.Workers, ProgressInterval: l.progressInterval(), OnProgress: l.progress(step)}
}

func (l *loader) progressInterval() time.Duration {
	if l.opts.ProgressInterval < 0 {
		return 0
	}
	return l.opts.ProgressInterval
}

func (l *loader) progress(step string) func(oldump.Progress) {
	return func(p oldump.Progress) {
		l.log.Info("reading", "step", step, "percent", fmt.Sprintf("%.1f", p.Percent()), "lines", p.Lines, "kept", p.Kept)
	}
}

// resolve follows Open Library redirects (merged records) for a full key such as "/works/OL1W".
func (l *loader) resolve(key string) string {
	for range 10 {
		next, ok := l.redirects[key]
		if !ok || next == key {
			return key
		}
		key = next
	}
	return key
}

const prepareSQL = `
TRUNCATE works, work_authors, editions, authors;
DROP TABLE IF EXISTS stage_works, stage_work_stats, stage_editions;
CREATE UNLOGGED TABLE stage_works (
    id text, title text, subtitle text, description text, first_publish_year smallint,
    subjects text[], subject_places text[], subject_people text[], subject_times text[],
    cover_id bigint, author_ids text[]
);
CREATE UNLOGGED TABLE stage_editions (
    id text, work_id text, title text, publish_year smallint, number_of_pages smallint,
    physical_format text, isbn_13 text[], isbn_10 text[], cover_id bigint
);
CREATE UNLOGGED TABLE stage_work_stats (
    id text, edition_count integer, english_edition_count integer, page_count smallint,
    original_language text, genres text[], edition_description text, first_sentence text,
    earliest_year smallint, cover_id bigint, rating_avg real, rating_count integer,
    want_to_read integer, currently_reading integer, already_read integer
);
`

// prepare drops secondary indexes (rebuilt in finalize), empties the catalogue and creates staging tables.
func (l *loader) prepare(ctx context.Context) error {
	var b strings.Builder
	for _, name := range db.IndexNames() {
		fmt.Fprintf(&b, "DROP INDEX IF EXISTS %s;\n", name)
	}
	b.WriteString(prepareSQL)
	_, err := l.pool.Exec(ctx, b.String())
	return err
}

var (
	typeRedirect = []byte("/type/redirect\t")
	typeWork     = []byte("/type/work\t")
	typeEdition  = []byte("/type/edition\t")
	typeAuthor   = []byte("/type/author\t")
	worksPrefix  = []byte("/works/")
	authPrefix   = []byte("/authors/")
)

func (l *loader) loadRedirects(ctx context.Context) error {
	path := l.opts.Sources.Redirects
	if path == "" {
		l.log.Info("no redirects dump; merged records will not be resolved")
		return nil
	}
	err := oldump.ReadLines(ctx, path, l.progressInterval(), l.progress("redirects"), func(line []byte) (bool, error) {
		if !bytes.HasPrefix(line, typeRedirect) {
			return false, nil
		}
		_, key, data, ok := oldump.SplitRecord(line)
		if !ok || !(bytes.HasPrefix(key, worksPrefix) || bytes.HasPrefix(key, authPrefix)) {
			return false, nil
		}
		var r struct {
			Location string `json:"location"`
		}
		if json.Unmarshal(data, &r) != nil || r.Location == "" {
			return false, nil
		}
		l.redirects[string(key)] = r.Location
		return true, nil
	})
	l.stats.Redirects = len(l.redirects)
	return err
}

type workRow struct {
	id, title, subtitle, description          string
	year                                      int
	subjects, places, people, times, authorID []string
	coverID                                   int64
}

const maxSubjects = 100

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
	r := workRow{
		id:          oldump.ShortKey(string(key)),
		title:       title,
		subtitle:    oldump.Clean(w.Subtitle),
		description: oldump.Clean(string(w.Description)),
		year:        oldump.ParseYear(w.FirstPublishDate, l.maxYear),
		subjects:    cleanList(w.Subjects, maxSubjects),
		places:      cleanList(w.SubjectPlaces, maxSubjects),
		people:      cleanList(w.SubjectPeople, maxSubjects),
		times:       cleanList(w.SubjectTimes, maxSubjects),
		coverID:     oldump.FirstCover(w.Covers),
		authorID:    []string{},
	}
	seen := map[string]bool{}
	for _, a := range w.Authors {
		k := l.resolve(a.Key)
		if !strings.HasPrefix(k, "/authors/") {
			continue
		}
		id := oldump.ShortKey(k)
		if id != "" && !seen[id] {
			seen[id] = true
			r.authorID = append(r.authorID, id)
		}
	}
	return r, true
}

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

type editionRow struct {
	id, workID, title, format string
	isbn13, isbn10            []string
	facts                     editionFacts
}

func normalizeISBNs(in []string) []string {
	out := []string{}
	for _, s := range in {
		if n := oldump.NormalizeISBN(s); n != "" && !contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (l *loader) editionWorkID(e *oldump.Edition, raw []byte) string {
	key := ""
	if len(e.Works) > 0 && e.Works[0] != "" {
		key = string(e.Works[0])
	} else if k, ok := oldump.EditionWorkKey(raw); ok {
		key = k
	}
	return oldump.ShortKey(l.resolve(key))
}

func (l *loader) decodeEdition(line []byte) (editionRow, bool) {
	_, key, data, ok := oldump.SplitRecord(line)
	if !ok {
		return editionRow{}, false
	}
	var e oldump.Edition
	if err := json.Unmarshal(data, &e); err != nil {
		l.decodeErrors.Add(1)
		return editionRow{}, false
	}
	workID := l.editionWorkID(&e, data)
	if _, ok := l.works[workID]; !ok {
		return editionRow{}, false
	}
	f := editionFacts{
		english:       e.HasLanguage("eng"),
		year:          oldump.ParseYear(e.PublishDate, l.maxYear),
		pages:         int(e.NumberOfPages),
		description:   oldump.Clean(string(e.Description)),
		firstSentence: oldump.Clean(string(e.FirstSentence)),
		coverID:       oldump.FirstCover(e.Covers),
		genres:        cleanList(e.Genres, maxGenres),
	}
	if len(e.Languages) > 0 {
		f.firstLanguage = oldump.ShortKey(string(e.Languages[0]))
	}
	for _, t := range e.TranslatedFrom {
		if code := oldump.ShortKey(string(t)); code != "" {
			f.translatedFrom = append(f.translatedFrom, code)
		}
	}
	title := oldump.Clean(e.Title)
	if title == "" {
		title = "(untitled)"
	}
	return editionRow{
		id:     oldump.ShortKey(string(key)),
		workID: workID,
		title:  title,
		format: oldump.Clean(e.PhysicalFormat),
		isbn13: normalizeISBNs(e.ISBN13),
		isbn10: normalizeISBNs(e.ISBN10),
		facts:  f,
	}, true
}

func (l *loader) loadEditions(ctx context.Context) error {
	// Runs on the reader goroutine: skip editions of non-candidate works without decoding JSON.
	filter := func(line []byte) bool {
		if !bytes.HasPrefix(line, typeEdition) {
			return false
		}
		key, ok := oldump.EditionWorkKey(line)
		if !ok {
			return false
		}
		_, ok = l.works[oldump.ShortKey(l.resolve(key))]
		return ok
	}
	s := oldump.NewStream(ctx, l.opts.Sources.Editions, l.streamOpts("editions"), filter, l.decodeEdition)
	cols := []string{"id", "work_id", "title", "publish_year", "number_of_pages", "physical_format",
		"isbn_13", "isbn_10", "cover_id"}
	n, err := copyStream(ctx, l.pool, "stage_editions", cols, s, func(r editionRow) []any {
		l.works[r.workID].addEdition(r.facts)
		l.stats.CandidateEditions++
		if !r.facts.english {
			return nil
		}
		pages := r.facts.pages
		if pages < 1 || pages > maxPages {
			pages = 0
		}
		return []any{r.id, r.workID, r.title, nullInt(r.facts.year), nullInt(pages), nullStr(r.format),
			r.isbn13, r.isbn10, nullInt64(r.facts.coverID)}
	})
	l.stats.EnglishEditions = n
	return err
}

type authorRow struct {
	id, name, birth, death, wikidata string
}

func (l *loader) loadAuthors(ctx context.Context) error {
	filter := func(line []byte) bool {
		if !bytes.HasPrefix(line, typeAuthor) {
			return false
		}
		_, key, _, ok := oldump.SplitRecord(line)
		if !ok {
			return false
		}
		_, needed := l.authorsNeeded[oldump.ShortKey(string(key))]
		return needed
	}
	decode := func(line []byte) (authorRow, bool) {
		_, key, data, ok := oldump.SplitRecord(line)
		if !ok {
			return authorRow{}, false
		}
		var a oldump.Author
		if err := json.Unmarshal(data, &a); err != nil {
			l.decodeErrors.Add(1)
			return authorRow{}, false
		}
		name := oldump.Clean(a.Name)
		if name == "" {
			return authorRow{}, false
		}
		return authorRow{
			id:       oldump.ShortKey(string(key)),
			name:     name,
			birth:    oldump.Clean(a.BirthDate),
			death:    oldump.Clean(a.DeathDate),
			wikidata: oldump.Clean(a.WikidataID()),
		}, true
	}
	s := oldump.NewStream(ctx, l.opts.Sources.Authors, l.streamOpts("authors"), filter, decode)
	seen := map[string]struct{}{}
	_, err := copyStream(ctx, l.pool, "authors", []string{"id", "name", "birth_date", "death_date", "wikidata_id"}, s,
		func(r authorRow) []any {
			if _, dup := seen[r.id]; dup {
				return nil
			}
			seen[r.id] = struct{}{}
			return []any{r.id, r.name, nullStr(r.birth), nullStr(r.death), nullStr(r.wikidata)}
		})
	return err
}

// statsLine handles the ratings and reading-log dumps: "work key \t edition key \t value \t date".
func (l *loader) statsLine(line []byte) (*workAgg, string, bool) {
	cols := bytes.Split(line, []byte{'\t'})
	if len(cols) < 3 {
		return nil, "", false
	}
	agg, ok := l.works[oldump.ShortKey(l.resolve(string(cols[0])))]
	if !ok {
		return nil, "", false
	}
	return agg, strings.TrimSpace(string(cols[2])), true
}

func (l *loader) loadRatings(ctx context.Context) error {
	if l.opts.Sources.Ratings == "" {
		l.log.Info("no ratings dump; skipping")
		return nil
	}
	return oldump.ReadLines(ctx, l.opts.Sources.Ratings, l.progressInterval(), l.progress("ratings"), func(line []byte) (bool, error) {
		agg, value, ok := l.statsLine(line)
		if !ok {
			return false, nil
		}
		rating, err := strconv.Atoi(value)
		if err != nil || rating < 1 || rating > 5 {
			return false, nil
		}
		agg.ratingSum += int32(rating)
		agg.ratingCount++
		l.stats.Ratings++
		return true, nil
	})
}

func (l *loader) loadReadingLog(ctx context.Context) error {
	if l.opts.Sources.ReadingLog == "" {
		l.log.Info("no reading log dump; skipping")
		return nil
	}
	return oldump.ReadLines(ctx, l.opts.Sources.ReadingLog, l.progressInterval(), l.progress("reading log"), func(line []byte) (bool, error) {
		agg, value, ok := l.statsLine(line)
		if !ok {
			return false, nil
		}
		switch strings.ReplaceAll(strings.ToLower(value), " ", "") {
		case "wanttoread":
			agg.wantToRead++
		case "currentlyreading":
			agg.currentlyReading++
		case "alreadyread":
			agg.alreadyRead++
		default:
			return false, nil
		}
		l.stats.ReadingLogEntries++
		return true, nil
	})
}

// copyWorkStats writes the per-work aggregates of candidates that have at least one English edition.
func (l *loader) copyWorkStats(ctx context.Context) error {
	ids := make([]string, 0, len(l.works))
	for id, a := range l.works {
		if a.english > 0 {
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
		var ratingAvg any
		if a.ratingCount > 0 {
			ratingAvg = float32(a.ratingSum) / float32(a.ratingCount)
		}
		genres := a.genres
		if genres == nil {
			genres = []string{}
		}
		return []any{id, a.editions, a.english, nullInt(a.medianPages()),
			nullStr(a.originalLanguage()), genres, nullStr(a.editionDesc), nullStr(a.firstSentence),
			nullInt(int(a.earliestYear)), nullInt64(a.coverID), ratingAvg, a.ratingCount,
			a.wantToRead, a.currentlyReading, a.alreadyRead}, true
	}}
	cols := []string{"id", "edition_count", "english_edition_count", "page_count", "original_language",
		"genres", "edition_description", "first_sentence", "earliest_year", "cover_id", "rating_avg",
		"rating_count", "want_to_read", "currently_reading", "already_read"}
	_, err := l.pool.CopyFrom(ctx, pgx.Identifier{"stage_work_stats"}, cols, src)
	return err
}

const finalizeSQL = `
INSERT INTO works (
    id, title, subtitle, description, description_source, first_sentence, first_publish_year,
    original_language, page_count, subjects, subject_places, subject_people, subject_times,
    genres, author_names, cover_id, edition_count, english_edition_count, rating_avg,
    rating_count, want_to_read, currently_reading, already_read, search_tsv
)
SELECT
    w.id, w.title, w.subtitle,
    coalesce(w.description, s.edition_description),
    CASE WHEN w.description IS NOT NULL THEN 'work'
         WHEN s.edition_description IS NOT NULL THEN 'edition' END,
    s.first_sentence,
    coalesce(w.first_publish_year, s.earliest_year),
    s.original_language, s.page_count,
    w.subjects, w.subject_places, w.subject_people, w.subject_times,
    s.genres, an.names, coalesce(w.cover_id, s.cover_id),
    s.edition_count, s.english_edition_count, s.rating_avg, s.rating_count,
    s.want_to_read, s.currently_reading, s.already_read,
    setweight(to_tsvector('english', w.title || ' ' || coalesce(w.subtitle, '')), 'A') ||
    setweight(to_tsvector('simple', array_to_string(an.names, ' ')), 'A') ||
    setweight(to_tsvector('english', left(array_to_string(
        w.subjects || s.genres || w.subject_places || w.subject_people || w.subject_times, ' '), 20000)), 'B') ||
    setweight(to_tsvector('english', left(coalesce(w.description, s.edition_description, ''), 20000)), 'C')
FROM stage_works w
JOIN stage_work_stats s ON s.id = w.id
CROSS JOIN LATERAL (
    SELECT coalesce(array_agg(a.name ORDER BY u.ord), '{}') AS names
    FROM unnest(w.author_ids) WITH ORDINALITY AS u(author_id, ord)
    JOIN authors a ON a.id = u.author_id
) an;

INSERT INTO work_authors (work_id, author_id, position)
SELECT w.id, u.author_id, (min(u.ord) - 1)::smallint
FROM stage_works w
JOIN works f ON f.id = w.id
CROSS JOIN LATERAL unnest(w.author_ids) WITH ORDINALITY AS u(author_id, ord)
GROUP BY w.id, u.author_id;

INSERT INTO editions (id, work_id, title, publish_year, number_of_pages, physical_format, isbn_13, isbn_10, cover_id)
SELECT DISTINCT ON (e.id) e.id, e.work_id, e.title, e.publish_year, e.number_of_pages,
       e.physical_format, e.isbn_13, e.isbn_10, e.cover_id
FROM stage_editions e
JOIN works w ON w.id = e.work_id
ORDER BY e.id;

DELETE FROM authors a WHERE NOT EXISTS (SELECT 1 FROM work_authors wa WHERE wa.author_id = a.id);

DROP TABLE stage_works, stage_work_stats, stage_editions;
`

func (l *loader) finalize(ctx context.Context) error {
	if _, err := l.pool.Exec(ctx, finalizeSQL); err != nil {
		return err
	}
	l.log.Info("building indexes")
	if _, err := l.pool.Exec(ctx, db.IndexesSQL()); err != nil {
		return fmt.Errorf("create indexes: %w", err)
	}
	if _, err := l.pool.Exec(ctx, "ANALYZE works, work_authors, editions, authors"); err != nil {
		return err
	}
	return l.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM works),
		(SELECT count(*) FROM works WHERE description IS NOT NULL),
		(SELECT count(*) FROM editions),
		(SELECT count(*) FROM authors),
		(SELECT count(*) FROM work_authors)`).Scan(
		&l.stats.Works, &l.stats.WorksWithDescription, &l.stats.Editions, &l.stats.Authors, &l.stats.WorkAuthors)
}

func (l *loader) recordRun(ctx context.Context, started time.Time) error {
	sources, err := json.Marshal(l.opts.Sources)
	if err != nil {
		return err
	}
	stats, err := json.Marshal(l.stats)
	if err != nil {
		return err
	}
	_, err = l.pool.Exec(ctx, "INSERT INTO ingest_runs (started_at, finished_at, sources, stats) VALUES ($1, $2, $3, $4)",
		started, time.Now(), sources, stats)
	return err
}

// copyStream COPYs rows built from a dump stream. toRow runs on the calling goroutine and may
// return nil to skip an item.
func copyStream[T any](ctx context.Context, pool *pgxpool.Pool, table string, cols []string, s *oldump.Stream[T], toRow func(T) []any) (int64, error) {
	src := &rowSource{next: func() ([]any, bool) {
		for v := range s.C {
			if row := toRow(v); row != nil {
				return row, true
			}
		}
		return nil, false
	}}
	n, err := pool.CopyFrom(ctx, pgx.Identifier{table}, cols, src)
	if err != nil {
		s.Stop()
		return n, fmt.Errorf("copy %s: %w", table, err)
	}
	return n, s.Err()
}

type rowSource struct {
	next func() ([]any, bool)
	row  []any
}

func (s *rowSource) Next() bool {
	row, ok := s.next()
	s.row = row
	return ok
}

func (s *rowSource) Values() ([]any, error) { return s.row, nil }
func (s *rowSource) Err() error             { return nil }

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
