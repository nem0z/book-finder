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

// keywordVectorSQL returns the $8 works whose keyword vector (work_keyword_embeddings), computed by model $2, is
// nearest to the $1 vector. Filters are applied while walking the HNSW index (pgvector iterative scans), so
// strict filters still return up to $8 results. relaxed_order may return rows slightly out of order, hence the
// outer sort. Bookshop works with the same ISBN-13 (the book at several shops) are kept once, the nearest, so a
// few less than $8 can come back; each work lists the shops that sell its ISBN-13.
const keywordVectorSQL = `
WITH nearest AS MATERIALIZED (
    SELECT w.id, w.title, w.author_names, coalesce(w.first_publish_year, 0) AS year,
           coalesce(w.page_count, 0) AS pages, coalesce(w.original_language, '') AS lang,
           coalesce(w.description, '') AS description, w.genres, w.subjects, coalesce(w.cover_id, 0) AS cover_id,
           w.rating_count,
           w.source, coalesce(w.source_url, '') AS source_url, coalesce(w.cover_url, '') AS cover_url, w.isbn_13,
           CASE WHEN w.source <> 'openlibrary' AND w.isbn_13 IS NOT NULL THEN w.isbn_13 ELSE w.id END AS book,
           e.embedding <=> $1::halfvec AS distance
    FROM work_keyword_embeddings e
    JOIN works w ON w.id = e.work_id
    WHERE e.model = $2
      AND ($3 = 0 OR w.first_publish_year >= $3)
      AND ($4 = 0 OR w.first_publish_year <= $4)
      AND ($5 = 0 OR w.page_count >= $5)
      AND ($6 = 0 OR w.page_count <= $6)
      AND ($7 = '' OR w.original_language = $7)
      AND (NOT $9 OR w.source <> 'openlibrary')
    ORDER BY e.embedding <=> $1::halfvec
    LIMIT $8
), books AS (
    SELECT DISTINCT ON (book) * FROM nearest ORDER BY book, distance
)
SELECT b.id, b.title, b.author_names, b.year, b.pages, b.lang, b.description, b.genres, b.subjects, b.cover_id,
       b.rating_count, b.source, b.source_url, b.cover_url,
       coalesce(s.sources, '{}'), coalesce(s.urls, '{}'), coalesce(s.in_stock, '{}'), 1 - b.distance
FROM books b
LEFT JOIN LATERAL (
    SELECT array_agg(o.source ORDER BY o.source) AS sources, array_agg(o.source_url ORDER BY o.source) AS urls,
           array_agg(coalesce(o.in_stock, false) ORDER BY o.source) AS in_stock
    FROM works o
    WHERE o.isbn_13 = b.isbn_13 AND o.source <> 'openlibrary'
) s ON true
ORDER BY b.distance`

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
	InShop             bool   // only works listed by a bookshop (works.source other than openlibrary)
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
	Source           string   `json:"source"`                 // works.source: openlibrary or a bookshop
	SourceURL        string   `json:"source_url,omitempty"`   // the bookshop's product page
	CoverURL         string   `json:"cover_url,omitempty"`    // the bookshop's cover
	Shops            []Offer  `json:"shops,omitempty"`        // the bookshops selling the work's ISBN-13
	Similarity       float64  `json:"similarity"`             // cosine similarity of the keyword vector, 1 = same direction
	RerankScore      float64  `json:"rerank_score,omitempty"` // set by Rerank; higher is more relevant
}

// Offer is a bookshop selling a book.
type Offer struct {
	Source  string `json:"source"` // the shop's ID (works.source)
	URL     string `json:"url"`
	InStock bool   `json:"in_stock"`
}

// KeywordVectors returns up to limit works matching f whose keyword vector is closest to the query's. A
// keyword vector embeds the keywords an LLM extracted from the work's description (cmd/index), which find
// more relevant books than the description itself did.
func KeywordVectors(ctx context.Context, pool *pgxpool.Pool, emb QueryEmbedder, query string, f Filters, limit int) ([]Result, error) {
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

	rows, err := tx.Query(ctx, keywordVectorSQL, embed.VectorLiteral(vec), emb.Model(), f.MinYear, f.MaxYear,
		f.MinPages, f.MaxPages, f.OriginalLanguage, limit, f.InShop)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	return scanResults(rows)
}

// scanResults reads keywordVectorSQL rows.
func scanResults(rows pgx.Rows) ([]Result, error) {
	var out []Result
	for rows.Next() {
		var r Result
		var year, pages int16
		var genres, subjects, shopSources, shopURLs []string
		var shopStock []bool
		if err := rows.Scan(&r.ID, &r.Title, &r.Authors, &year, &pages, &r.OriginalLanguage, &r.Description,
			&genres, &subjects, &r.CoverID, &r.RatingCount, &r.Source, &r.SourceURL, &r.CoverURL,
			&shopSources, &shopURLs, &shopStock, &r.Similarity); err != nil {
			return nil, err
		}

		r.Year, r.Pages, r.Tags = int(year), int(pages), embed.Topics(genres, subjects)
		for i, s := range shopSources {
			r.Shops = append(r.Shops, Offer{Source: s, URL: shopURLs[i], InStock: shopStock[i]})
		}

		out = append(out, r)
	}

	return out, rows.Err()
}
