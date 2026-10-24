// Package search finds the books closest in meaning to a free-text description.
package search

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/embed"
)

const (
	DefaultLimit = 10

	// efSearch is the HNSW candidate list size per query: higher finds more of the true nearest
	// neighbours, slower.
	efSearch = 200
)

var (
	// semanticSQL searches the vectors of the descriptions.
	semanticSQL = nearestSQL("work_embeddings")

	// keywordVectorSQL searches the vectors of the LLM keywords.
	keywordVectorSQL = nearestSQL("work_keyword_embeddings")
)

// nearestSQL returns the $8 works whose vector in table, computed by model $2, is nearest to the $1 vector
// (among the $9 works, when not NULL). Filters are applied while walking the HNSW index (pgvector iterative
// scans), so strict filters still return up to $8 results. relaxed_order may return rows slightly out of
// order, hence the outer sort.
func nearestSQL(table string) string {
	return `
WITH nearest AS MATERIALIZED (
    SELECT w.id, w.title, w.author_names, coalesce(w.first_publish_year, 0) AS year,
           coalesce(w.page_count, 0) AS pages, coalesce(w.original_language, '') AS lang,
           w.description, w.genres, w.subjects, coalesce(w.cover_id, 0) AS cover_id, w.rating_count,
           e.embedding <=> $1::halfvec AS distance
    FROM ` + table + ` e
    JOIN works w ON w.id = e.work_id
    WHERE e.model = $2
      AND ($3 = 0 OR w.first_publish_year >= $3)
      AND ($4 = 0 OR w.first_publish_year <= $4)
      AND ($5 = 0 OR w.page_count >= $5)
      AND ($6 = 0 OR w.page_count <= $6)
      AND ($7 = '' OR w.original_language = $7)
      AND ($9::text[] IS NULL OR w.id = ANY($9))
    ORDER BY e.embedding <=> $1::halfvec
    LIMIT $8
)
SELECT id, title, author_names, year, pages, lang, description, genres, subjects, cover_id, rating_count,
       1 - distance
FROM nearest
ORDER BY distance`
}

var ErrEmptyQuery = errors.New("empty query")

// QueryEmbedder turns a search query into a vector. *embed.Ollama implements it.
type QueryEmbedder interface {
	Model() string
	EmbedQuery(ctx context.Context, query string) ([]float32, error)
}

// Filters are hard constraints. Zero values mean no constraint.
type Filters struct {
	MinYear, MaxYear   int
	MinPages, MaxPages int
	OriginalLanguage   string // MARC code, e.g. "eng", "jpn"
	// IDs restricts the search to these works, e.g. the sample a benchmark runs on.
	IDs []string
}

type Result struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Authors          []string `json:"authors"`
	Year             int      `json:"year,omitempty"`
	Pages            int      `json:"pages,omitempty"`
	OriginalLanguage string   `json:"original_language,omitempty"`
	Description      string   `json:"description"`
	Tags             []string `json:"tags"`               // genres then subjects, without generic ones
	CoverID          int64    `json:"cover_id,omitempty"` // Open Library cover ID
	RatingCount      int      `json:"rating_count"`
	Similarity       float64  `json:"similarity"`              // cosine similarity, 1 = same direction; set by Semantic and KeywordVectors
	LexicalScore     float64  `json:"lexical_score,omitempty"` // BM25 score; set by Keywords and FullText
	RerankScore      float64  `json:"rerank_score,omitempty"`  // set by Rerank; higher is more relevant
}

// Semantic returns up to limit works whose description vector is closest to the query's and that match f.
func Semantic(ctx context.Context, pool *pgxpool.Pool, emb QueryEmbedder, query string, f Filters, limit int) ([]Result, error) {
	return nearest(ctx, pool, semanticSQL, emb, query, f, limit)
}

// KeywordVectors returns up to limit works whose LLM keywords' vector (work_keyword_embeddings) is closest to
// the query's and that match f.
func KeywordVectors(ctx context.Context, pool *pgxpool.Pool, emb QueryEmbedder, query string, f Filters, limit int) ([]Result, error) {
	return nearest(ctx, pool, keywordVectorSQL, emb, query, f, limit)
}

func nearest(ctx context.Context, pool *pgxpool.Pool, sql string, emb QueryEmbedder, query string, f Filters, limit int) ([]Result, error) {
	if strings.TrimSpace(query) == "" {
		return nil, ErrEmptyQuery
	}

	if limit <= 0 {
		limit = DefaultLimit
	}

	vec, err := emb.EmbedQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only; nothing to keep

	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL hnsw.ef_search = %d; SET LOCAL hnsw.iterative_scan = relaxed_order", efSearch)); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, sql, embed.VectorLiteral(vec), emb.Model(), f.MinYear, f.MaxYear,
		f.MinPages, f.MaxPages, f.OriginalLanguage, limit, f.ids())
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	return scanResults(rows, func(r *Result) *float64 { return &r.Similarity })
}

// ids returns f.IDs for SQL: NULL, meaning no constraint, when there are none.
func (f Filters) ids() []string {
	if len(f.IDs) == 0 {
		return nil
	}

	return f.IDs
}

// scanResults reads result rows: the work's columns, then its score, stored where score points.
func scanResults(rows pgx.Rows, score func(*Result) *float64) ([]Result, error) {
	var out []Result
	for rows.Next() {
		var r Result
		var year, pages int16
		var genres, subjects []string
		if err := rows.Scan(&r.ID, &r.Title, &r.Authors, &year, &pages, &r.OriginalLanguage, &r.Description,
			&genres, &subjects, &r.CoverID, &r.RatingCount, score(&r)); err != nil {
			return nil, err
		}

		r.Year, r.Pages, r.Tags = int(year), int(pages), embed.Topics(genres, subjects)
		out = append(out, r)
	}

	return out, rows.Err()
}
