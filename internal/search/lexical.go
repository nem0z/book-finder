package search

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// lexicalSource is a full-text vector to search. Document statistics are computed over the whole stats
// table; candidates come from `from`, which joins the vector to works as w, restricted to works with an
// embedding so that every retrieval searches the same catalogue.
type lexicalSource struct {
	stats, statsTSV, from, tsv string
}

var (
	// keywordSQL searches the keywords an LLM extracted from each work (work_keywords).
	keywordSQL = lexicalSQL(lexicalSource{stats: "work_keywords", statsTSV: "keywords_tsv",
		from: "work_keywords d JOIN works w ON w.id = d.work_id", tsv: "d.keywords_tsv"})

	// fullTextSQL searches works.search_tsv (title, authors, subjects and description): keyword search
	// without an LLM, to compare with.
	fullTextSQL = lexicalSQL(lexicalSource{stats: "works", statsTSV: "search_tsv",
		from: "works w JOIN work_embeddings d ON d.work_id = w.id", tsv: "w.search_tsv"})
)

// Keywords returns up to limit works whose LLM keywords best match the words of query, and that match f.
func Keywords(ctx context.Context, pool *pgxpool.Pool, query string, f Filters, limit int) ([]Result, error) {
	return lexical(ctx, pool, keywordSQL, query, f, limit)
}

// FullText returns up to limit works whose title, authors, subjects and description best match the words
// of query, and that match f.
func FullText(ctx context.Context, pool *pgxpool.Pool, query string, f Filters, limit int) ([]Result, error) {
	return lexical(ctx, pool, fullTextSQL, query, f, limit)
}

func lexical(ctx context.Context, pool *pgxpool.Pool, sql, query string, f Filters, limit int) ([]Result, error) {
	if strings.TrimSpace(query) == "" {
		return nil, ErrEmptyQuery
	}

	if limit <= 0 {
		limit = DefaultLimit
	}

	rows, err := pool.Query(ctx, sql, query, f.MinYear, f.MaxYear, f.MinPages, f.MaxPages, f.OriginalLanguage, limit)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	defer rows.Close()

	return scanResults(rows, func(r *Result) *float64 { return &r.LexicalScore })
}

// lexicalSQL returns the $7 works of s that best match the words of the $1 query, with BM25 (k1 = 1.2,
// b = 0.75) for a term frequency of 1: a work scores the sum of the IDF of the query's lexemes it
// contains, times 2.2 / (1.3 + 0.9 × its lexeme count / the mean count). Postgres's ts_rank has no IDF,
// so common words ("man", "young") would outweigh rare ones. Stop words are dropped by to_tsvector.
func lexicalSQL(s lexicalSource) string {
	return fmt.Sprintf(`
WITH stats AS MATERIALIZED (
    SELECT count(*)::float8 AS n, coalesce(avg(length(%[2]s)), 1)::float8 AS avgdl FROM %[1]s
),
terms AS MATERIALIZED (
    SELECT q.tq, ln(1 + (s.n - q.docs + 0.5) / (q.docs + 0.5)) AS idf
    FROM stats s, (
        SELECT l.tq, (SELECT count(*) FROM %[1]s WHERE %[2]s @@ l.tq) AS docs
        FROM (SELECT DISTINCT quote_literal(lexeme)::tsquery AS tq FROM unnest(to_tsvector('english', $1))) l
    ) q
    WHERE q.docs > 0
),
best AS MATERIALIZED (
    SELECT w.id, sum(t.idf) * 2.2 / (1.3 + 0.9 * length(%[4]s) / s.avgdl) AS score
    FROM %[3]s
    JOIN terms t ON %[4]s @@ t.tq
    CROSS JOIN stats s
    WHERE ($2 = 0 OR w.first_publish_year >= $2)
      AND ($3 = 0 OR w.first_publish_year <= $3)
      AND ($4 = 0 OR w.page_count >= $4)
      AND ($5 = 0 OR w.page_count <= $5)
      AND ($6 = '' OR w.original_language = $6)
    GROUP BY w.id, length(%[4]s), s.avgdl
    ORDER BY score DESC, w.rating_count DESC, w.id
    LIMIT $7
)
SELECT w.id, w.title, w.author_names, coalesce(w.first_publish_year, 0), coalesce(w.page_count, 0),
       coalesce(w.original_language, ''), w.description, w.genres, w.subjects, coalesce(w.cover_id, 0),
       w.rating_count, b.score
FROM best b
JOIN works w ON w.id = b.id
ORDER BY b.score DESC, w.rating_count DESC, w.id`, s.stats, s.statsTSV, s.from, s.tsv)
}
