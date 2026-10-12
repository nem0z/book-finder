// Package search finds books matching a free-text description.
package search

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/embed"
)

// Filters are hard constraints applied alongside the vector search. Zero values mean "no constraint".
type Filters struct {
	MinYear, MaxYear   int
	MinPages, MaxPages int
	OriginalLanguage   string // MARC code, e.g. "eng", "jpn"
	ExcludeIDs         []string
}

type Result struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Authors          []string `json:"authors"`
	Year             int      `json:"year,omitempty"`
	Pages            int      `json:"pages,omitempty"`
	OriginalLanguage string   `json:"original_language,omitempty"`
	Description      string   `json:"description,omitempty"`
	CoverID          int64    `json:"cover_id,omitempty"`
	Readers          int      `json:"readers"`
	Similarity       float64  `json:"similarity"` // cosine similarity, 1 = identical direction
}

// EfSearch is the HNSW candidate list size per query: higher = better recall, slower.
const EfSearch = 200

// Semantic embeds query and returns the limit nearest works by cosine distance that satisfy f.
//
// Filters are applied while walking the HNSW graph using pgvector's iterative index scans, so
// selective filters still return up to limit results instead of an under-filled list.
func Semantic(ctx context.Context, pool *pgxpool.Pool, emb embed.Embedder, query string, f Filters, limit int) ([]Result, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty query")
	}
	if limit <= 0 {
		limit = 10
	}
	vec, err := emb.EmbedQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	args := []any{embed.VectorLiteral(vec), emb.Model()}
	where := []string{"e.model = $2"}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.MinYear > 0 {
		add("w.first_publish_year >= ?", f.MinYear)
	}
	if f.MaxYear > 0 {
		add("w.first_publish_year <= ?", f.MaxYear)
	}
	if f.MinPages > 0 {
		add("w.page_count >= ?", f.MinPages)
	}
	if f.MaxPages > 0 {
		add("w.page_count <= ?", f.MaxPages)
	}
	if f.OriginalLanguage != "" {
		add("w.original_language = ?", f.OriginalLanguage)
	}
	if len(f.ExcludeIDs) > 0 {
		add("NOT (w.id = ANY(?))", f.ExcludeIDs)
	}
	args = append(args, limit)

	// relaxed_order iterative scans may return rows slightly out of order, hence the outer sort.
	sql := fmt.Sprintf(`
WITH nearest AS MATERIALIZED (
    SELECT w.id, w.title, w.author_names, coalesce(w.first_publish_year, 0) AS year,
           coalesce(w.page_count, 0) AS pages, coalesce(w.original_language, '') AS lang,
           coalesce(w.description, '') AS description, coalesce(w.cover_id, 0) AS cover_id,
           w.want_to_read + w.currently_reading + w.already_read AS readers,
           e.embedding <=> $1::halfvec AS distance
    FROM work_embeddings e
    JOIN works w ON w.id = e.work_id
    WHERE %s
    ORDER BY e.embedding <=> $1::halfvec
    LIMIT $%d
)
SELECT id, title, author_names, year, pages, lang, description, cover_id, readers, 1 - distance
FROM nearest
ORDER BY distance`, strings.Join(where, " AND "), len(args))

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL hnsw.ef_search = %d; SET LOCAL hnsw.iterative_scan = relaxed_order", EfSearch)); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Result
	for rows.Next() {
		var r Result
		var year, pages int16
		var readers int32
		if err := rows.Scan(&r.ID, &r.Title, &r.Authors, &year, &pages, &r.OriginalLanguage,
			&r.Description, &r.CoverID, &readers, &r.Similarity); err != nil {
			return nil, err
		}
		r.Year, r.Pages, r.Readers = int(year), int(pages), int(readers)
		out = append(out, r)
	}
	return out, rows.Err()
}
