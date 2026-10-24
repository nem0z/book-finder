package embed

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"
)

// keywordsSQL reads the works with LLM keywords after the $1 ID, in ID order: the keywords, places,
// themes, period and protagonist type as one text (the order enrich.Keywords.Text uses), and the hash
// of their current vector.
const keywordsSQL = `
SELECT k.work_id,
       array_to_string(k.keywords || k.setting_places || k.themes || array_remove(ARRAY[k.era_label, k.protagonist], ''), '; '),
       coalesce(e.content_hash, '')
FROM work_keywords k
LEFT JOIN work_keyword_embeddings e ON e.work_id = k.work_id
WHERE k.work_id > $1
ORDER BY k.work_id
LIMIT $2`

const upsertKeywordsSQL = `
INSERT INTO work_keyword_embeddings (work_id, model, content_hash, embedding)
SELECT t.id, $2, t.hash, t.vec::halfvec
FROM unnest($1::text[], $3::text[], $4::text[]) AS t(id, hash, vec)
ON CONFLICT (work_id) DO UPDATE
SET model = EXCLUDED.model, content_hash = EXCLUDED.content_hash, embedding = EXCLUDED.embedding,
    created_at = now()`

const pruneKeywordsSQL = `
DELETE FROM work_keyword_embeddings e WHERE NOT EXISTS (SELECT 1 FROM work_keywords k WHERE k.work_id = e.work_id)`

// RunKeywords embeds the LLM keywords of every work that has some, when their vector is missing or was
// computed from another model or text, then deletes the vectors of works without keywords. The keywords
// are embedded as a document without a title.
func RunKeywords(ctx context.Context, pool *pgxpool.Pool, emb Embedder, opts Options) (Stats, error) {
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultBatchSize
	}

	var stats Stats
	for last := ""; ; {
		jobs, read, next, err := readKeywordPage(ctx, pool, emb.Model(), last)
		if err != nil {
			return stats, err
		}

		stats.Considered += int64(read)
		stats.UpToDate += int64(read - len(jobs))
		for batch := range slices.Chunk(jobs, opts.BatchSize) {
			if err := embedKeywordBatch(ctx, pool, emb, batch); err != nil {
				return stats, err
			}

			stats.Embedded += int64(len(batch))
		}

		if read < defaultPageSize {
			break
		}

		last = next
	}

	tag, err := pool.Exec(ctx, pruneKeywordsSQL)
	if err != nil {
		return stats, fmt.Errorf("prune keyword vectors: %w", err)
	}

	stats.Pruned = tag.RowsAffected()

	return stats, nil
}

// readKeywordPage reads a page of works with keywords after last, and returns the jobs for those whose
// vector is missing or stale, how many works it read and the last ID.
func readKeywordPage(ctx context.Context, pool *pgxpool.Pool, model, last string) ([]job, int, string, error) {
	rows, err := pool.Query(ctx, keywordsSQL, last, defaultPageSize)
	if err != nil {
		return nil, 0, "", fmt.Errorf("read keywords: %w", err)
	}
	defer rows.Close()

	var jobs []job
	read := 0
	for rows.Next() {
		var id, text, hash string
		if err := rows.Scan(&id, &text, &hash); err != nil {
			return nil, 0, "", fmt.Errorf("read keywords: %w", err)
		}

		read, last = read+1, id
		doc := Doc{Text: text}
		if want := ContentHash(model, doc); want != hash {
			jobs = append(jobs, job{id: id, hash: want, doc: doc})
		}
	}

	return jobs, read, last, rows.Err()
}

func embedKeywordBatch(ctx context.Context, pool *pgxpool.Pool, emb Embedder, batch []job) error {
	docs := make([]Doc, len(batch))
	for i, j := range batch {
		docs[i] = j.doc
	}

	vecs, err := emb.EmbedDocuments(ctx, docs)
	if err != nil {
		return err
	}

	if len(vecs) != len(batch) {
		return fmt.Errorf("%w: %d vectors for %d texts", ErrBadResponse, len(vecs), len(batch))
	}

	ids, hashes, literals := make([]string, len(batch)), make([]string, len(batch)), make([]string, len(batch))
	for i, j := range batch {
		ids[i], hashes[i], literals[i] = j.id, j.hash, VectorLiteral(vecs[i])
	}

	if _, err := pool.Exec(ctx, upsertKeywordsSQL, ids, emb.Model(), hashes, literals); err != nil {
		return fmt.Errorf("store keyword vectors: %w", err)
	}

	return nil
}
