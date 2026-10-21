package enrich

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pendingSQL reads works with a vector, most-rated first, after the ($1, $2) position in that order, and
// only those in $3 when it is not empty. Keywords are extracted for the works vector search covers, so
// that both searches are compared on the same catalogue.
const pendingSQL = `
SELECT w.id, w.title, w.author_names, w.description, w.rating_count, coalesce(k.content_hash, '')
FROM works w
JOIN work_embeddings e ON e.work_id = w.id
LEFT JOIN work_keywords k ON k.work_id = w.id
WHERE (-w.rating_count, w.id) > ($1, $2)
  AND (coalesce(cardinality($3::text[]), 0) = 0 OR w.id = ANY($3))
ORDER BY -w.rating_count, w.id
LIMIT $4`

const upsertSQL = `
INSERT INTO work_keywords (work_id, model, content_hash, keywords, setting_places, era_from, era_to, era_label,
                           themes, protagonist, keywords_tsv)
VALUES ($1, $2, $3, $4, $5, nullif($6, 0), nullif($7, 0), $8, $9, $10, to_tsvector('english', $11))
ON CONFLICT (work_id) DO UPDATE
SET model = EXCLUDED.model, content_hash = EXCLUDED.content_hash, keywords = EXCLUDED.keywords,
    setting_places = EXCLUDED.setting_places, era_from = EXCLUDED.era_from, era_to = EXCLUDED.era_to,
    era_label = EXCLUDED.era_label, themes = EXCLUDED.themes, protagonist = EXCLUDED.protagonist,
    keywords_tsv = EXCLUDED.keywords_tsv, created_at = now()`

const pruneSQL = `DELETE FROM work_keywords k WHERE NOT EXISTS (SELECT 1 FROM works w WHERE w.id = k.work_id)`

var ErrEmptyCatalogue = errors.New("no works in the catalogue; load it first")

// Cursor is the position after the last work read, in pendingSQL order. The zero value is before the
// first work.
type Cursor struct {
	negRatings int
	id         string
	started    bool
}

// Page is what ReadPending read: the works whose keywords are missing or stale, the position after the
// last work, and how many works were read in total.
type Page struct {
	Books []Book
	Next  Cursor
	Read  int
}

// Extracted is a work's keywords, ready to store.
type Extracted struct {
	WorkID   string
	Hash     string
	Keywords Keywords
}

// ReadPending reads up to size works after c (only those in ids when ids is not empty), and returns
// the ones whose keywords are missing or were extracted by another model, prompt or text.
func ReadPending(ctx context.Context, pool *pgxpool.Pool, model string, ids []string, c Cursor, size int) (Page, error) {
	if !c.started {
		c = Cursor{negRatings: math.MinInt32, started: true}
	}

	p := Page{Next: c}
	rows, err := pool.Query(ctx, pendingSQL, c.negRatings, c.id, ids, size)
	if err != nil {
		return p, fmt.Errorf("read works: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var b Book
		var ratings int
		var hash string
		if err := rows.Scan(&b.ID, &b.Title, &b.Authors, &b.Description, &ratings, &hash); err != nil {
			return p, fmt.Errorf("read works: %w", err)
		}

		p.Read++
		p.Next = Cursor{negRatings: -ratings, id: b.ID, started: true}
		if b.Hash = ContentHash(model, b); b.Hash != hash {
			p.Books = append(p.Books, b)
		}
	}

	return p, rows.Err()
}

// Save stores keywords extracted by model, replacing older ones.
func Save(ctx context.Context, pool *pgxpool.Pool, model string, rows []Extracted) error {
	if len(rows) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, r := range rows {
		k := r.Keywords.Normalize()
		batch.Queue(upsertSQL, r.WorkID, model, r.Hash, k.Keywords, k.Places, k.EraFrom, k.EraTo, k.EraLabel,
			k.Themes, k.Protagonist, k.Text())
	}

	if err := pool.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("store keywords: %w", err)
	}

	return nil
}

// Prune deletes the keywords of works no longer in the catalogue and returns how many. It refuses to run
// on an empty catalogue (e.g. during a reload), which would delete everything.
func Prune(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var works int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM works").Scan(&works); err != nil {
		return 0, err
	}

	if works == 0 {
		return 0, ErrEmptyCatalogue
	}

	tag, err := pool.Exec(ctx, pruneSQL)
	if err != nil {
		return 0, fmt.Errorf("prune keywords: %w", err)
	}

	return tag.RowsAffected(), nil
}
