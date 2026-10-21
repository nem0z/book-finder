-- LLM keywords per work, for keyword search: the words a reader might use to describe the book (places,
-- period, themes, tropes, mood, type of protagonist), with common synonyms and word forms, plus a few
-- structured facts about its setting. Extracted by cmd/enrich from the title, authors and description.
--
-- No foreign key to works, like work_embeddings: the loader empties works on every rebuild, and
-- extraction takes hours and costs money, so keywords survive rebuilds. cmd/enrich only extracts works
-- whose text changed (content_hash) and deletes rows whose work is gone.

CREATE TABLE work_keywords (
    work_id        text PRIMARY KEY,
    model          text NOT NULL,
    -- Model, prompt version and the work's text the keywords were extracted from.
    content_hash   text NOT NULL,
    keywords       text[] NOT NULL,
    setting_places text[] NOT NULL DEFAULT '{}',
    -- Years the story takes place (negative for BC), not publication years. NULL when unknown.
    era_from       smallint,
    era_to         smallint,
    era_label      text NOT NULL DEFAULT '',
    themes         text[] NOT NULL DEFAULT '{}',
    protagonist    text NOT NULL DEFAULT '',
    -- Every field above as one English full-text vector.
    keywords_tsv   tsvector NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
