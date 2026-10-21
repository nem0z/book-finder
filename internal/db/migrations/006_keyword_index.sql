-- Keyword search on the LLM keywords. Not in 003_indexes.sql: the loader drops and rebuilds those, and
-- work_keywords survives rebuilds.
CREATE INDEX work_keywords_tsv_idx ON work_keywords USING gin (keywords_tsv);
