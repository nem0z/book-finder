-- The full-text vector of the LLM keywords and its index (from a 006 migration no longer in this repository) served
-- a BM25 keyword search that was not kept: search compares keyword vectors (work_keyword_embeddings).
DROP INDEX IF EXISTS work_keywords_tsv_idx;
ALTER TABLE work_keywords DROP COLUMN IF EXISTS keywords_tsv;
