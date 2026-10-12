-- Secondary indexes. The loader drops these before a bulk load and re-runs this file afterwards,
-- so every statement must be idempotent and named.

-- Keyword search over title, authors, subjects, genres and description.
CREATE INDEX IF NOT EXISTS works_search_tsv_idx ON works USING gin (search_tsv);

-- Fuzzy title / author lookup, e.g. resolving "like The Remains of the Day".
CREATE INDEX IF NOT EXISTS works_title_trgm_idx ON works USING gin (lower(title) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS authors_name_trgm_idx ON authors USING gin (lower(name) gin_trgm_ops);

-- Hard filters extracted from the user's query.
CREATE INDEX IF NOT EXISTS works_first_publish_year_idx ON works (first_publish_year);
CREATE INDEX IF NOT EXISTS works_page_count_idx ON works (page_count);
CREATE INDEX IF NOT EXISTS works_original_language_idx ON works (original_language);
CREATE INDEX IF NOT EXISTS works_genres_idx ON works USING gin (genres);
CREATE INDEX IF NOT EXISTS works_subjects_idx ON works USING gin (subjects);

-- Popularity ordering / tie-breaking: reading-log entries plus number of ratings.
CREATE INDEX IF NOT EXISTS works_popularity_idx
    ON works ((want_to_read + currently_reading + already_read + rating_count) DESC);

-- Joins and lookups.
CREATE INDEX IF NOT EXISTS work_authors_author_idx ON work_authors (author_id);
CREATE INDEX IF NOT EXISTS works_isbn_13_idx ON works (isbn_13);
