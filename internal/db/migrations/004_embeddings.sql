-- Semantic search: one EmbeddingGemma vector per work, compared by cosine distance.
--
-- No foreign key to works on purpose: the loader empties works on every rebuild, and vectors take
-- hours to compute, so they survive rebuilds. cmd/embed only recomputes works whose text changed
-- (content_hash) and deletes rows whose work is gone.
--
-- halfvec (16-bit floats) halves the table and index size compared with vector, with no meaningful
-- loss of search quality. 768 is EmbeddingGemma's size; another size needs a new migration.

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE work_embeddings (
    work_id      text PRIMARY KEY,
    model        text NOT NULL,
    content_hash text NOT NULL,
    embedding    halfvec(768) NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Approximate nearest-neighbour index. m and ef_construction are pgvector's defaults, a good
-- recall/speed trade-off at a few hundred thousand rows. Search quality is tuned with hnsw.ef_search.
-- Not in 003_indexes.sql: the loader drops and rebuilds those, and this one must survive rebuilds.
CREATE INDEX work_embeddings_hnsw_idx ON work_embeddings
    USING hnsw (embedding halfvec_cosine_ops) WITH (m = 16, ef_construction = 64);
