package embed

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

const upsertKeywordsSQL = `
INSERT INTO work_keyword_embeddings (work_id, model, content_hash, embedding)
SELECT t.id, $2, t.hash, t.vec::halfvec
FROM unnest($1::text[], $3::text[], $4::text[]) AS t(id, hash, vec)
ON CONFLICT (work_id) DO UPDATE
SET model = EXCLUDED.model, content_hash = EXCLUDED.content_hash, embedding = EXCLUDED.embedding,
    created_at = now()`

const pruneKeywordsSQL = `
DELETE FROM work_keyword_embeddings e WHERE NOT EXISTS (SELECT 1 FROM works w WHERE w.id = e.work_id)`

// KeywordText is a work's LLM keywords as one text (enrich.Keywords.Text), to embed.
type KeywordText struct {
	ID   string
	Text string
}

// KeywordDoc is what a keyword vector embeds: the keywords as a document without a title.
func KeywordDoc(text string) Doc { return Doc{Text: text} }

// KeywordHash is the content hash of the keyword vector of text by model, as stored in
// work_keyword_embeddings: a vector with another hash is missing or stale.
func KeywordHash(model, text string) string { return ContentHash(model, KeywordDoc(text)) }

// StoreKeywordVectors embeds texts with emb in one call and stores the vectors in work_keyword_embeddings,
// replacing older ones.
func StoreKeywordVectors(ctx context.Context, pool *pgxpool.Pool, emb Embedder, texts []KeywordText) error {
	if len(texts) == 0 {
		return nil
	}

	docs := make([]Doc, len(texts))
	for i, t := range texts {
		docs[i] = KeywordDoc(t.Text)
	}

	vecs, err := emb.EmbedDocuments(ctx, docs)
	if err != nil {
		return err
	}

	if len(vecs) != len(texts) {
		return fmt.Errorf("%w: %d vectors for %d texts", ErrBadResponse, len(vecs), len(texts))
	}

	ids, hashes, literals := make([]string, len(texts)), make([]string, len(texts)), make([]string, len(texts))
	for i, t := range texts {
		ids[i], hashes[i], literals[i] = t.ID, KeywordHash(emb.Model(), t.Text), VectorLiteral(vecs[i])
	}

	if _, err := pool.Exec(ctx, upsertKeywordsSQL, ids, emb.Model(), hashes, literals); err != nil {
		return fmt.Errorf("store keyword vectors: %w", err)
	}

	return nil
}

// PruneKeywordVectors deletes the keyword vectors of works no longer in the catalogue and returns how many.
// It refuses to run on an empty catalogue (e.g. during a reload), which would delete every vector.
func PruneKeywordVectors(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var works int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM works").Scan(&works); err != nil {
		return 0, err
	}

	if works == 0 {
		return 0, ErrEmptyCatalogue
	}

	tag, err := pool.Exec(ctx, pruneKeywordsSQL)
	if err != nil {
		return 0, fmt.Errorf("prune keyword vectors: %w", err)
	}

	return tag.RowsAffected(), nil
}
