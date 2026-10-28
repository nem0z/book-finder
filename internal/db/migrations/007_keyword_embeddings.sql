-- One EmbeddingGemma vector per work's LLM keywords (work_keywords), for searching the keywords by
-- meaning rather than by word. Kept apart from work_embeddings, whose vectors embed the description.
-- No foreign key, like work_keywords: both survive catalogue reloads.

CREATE TABLE work_keyword_embeddings (
    work_id      text PRIMARY KEY,
    model        text NOT NULL,
    content_hash text NOT NULL,
    embedding    halfvec(768) NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX work_keyword_embeddings_hnsw_idx ON work_keyword_embeddings
    USING hnsw (embedding halfvec_cosine_ops) WITH (m = 16, ef_construction = 64);
