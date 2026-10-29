package enrich

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const upsertSQL = `
INSERT INTO work_keywords (work_id, model, content_hash, keywords, setting_places, era_from, era_to, era_label,
                           themes, protagonist)
VALUES ($1, $2, $3, $4, $5, nullif($6, 0), nullif($7, 0), $8, $9, $10)
ON CONFLICT (work_id) DO UPDATE
SET model = EXCLUDED.model, content_hash = EXCLUDED.content_hash, keywords = EXCLUDED.keywords,
    setting_places = EXCLUDED.setting_places, era_from = EXCLUDED.era_from, era_to = EXCLUDED.era_to,
    era_label = EXCLUDED.era_label, themes = EXCLUDED.themes, protagonist = EXCLUDED.protagonist,
    created_at = now()`

const pruneSQL = `DELETE FROM work_keywords k WHERE NOT EXISTS (SELECT 1 FROM works w WHERE w.id = k.work_id)`

var ErrEmptyCatalogue = errors.New("no works in the catalogue; load it first")

// Extracted is a work's keywords, ready to store.
type Extracted struct {
	WorkID   string
	Hash     string
	Keywords Keywords
}

// Save stores keywords extracted by model, replacing older ones.
func Save(ctx context.Context, pool *pgxpool.Pool, model string, rows []Extracted) error {
	batch := &pgx.Batch{}
	for _, r := range rows {
		k := r.Keywords.Normalize()
		batch.Queue(upsertSQL, r.WorkID, model, r.Hash, k.Keywords, k.Places, k.EraFrom, k.EraTo, k.EraLabel,
			k.Themes, k.Protagonist)
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
