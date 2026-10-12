-- Semantic search: one embedding per work, compared by cosine distance.
--
-- Not a foreign key to works on purpose: the loader truncates works on every rebuild, and embeddings
-- are slow to compute, so they survive rebuilds. cmd/embed re-embeds only works whose text changed
-- (content_hash) and prunes rows whose work no longer exists.
--
-- halfvec (16-bit floats) halves storage and index size versus vector with no meaningful recall loss.
-- 768 dimensions matches the default model (EmbeddingGemma); a different size needs a new migration.

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE work_embeddings (
    work_id      text PRIMARY KEY,
    model        text NOT NULL,
    content_hash text NOT NULL,
    embedding    halfvec(768) NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Approximate nearest-neighbour index. m / ef_construction are pgvector defaults, a good recall/speed
-- trade-off at a few hundred thousand rows. Query-time recall is tuned with hnsw.ef_search.
CREATE INDEX work_embeddings_hnsw_idx ON work_embeddings
    USING hnsw (embedding halfvec_cosine_ops) WITH (m = 16, ef_construction = 64);
