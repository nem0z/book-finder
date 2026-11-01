-- Where a work comes from. Open Library works are rebuilt by cmd/ingest; bookshop works (one row per book the
-- shop lists, id '<source>:<isbn13>') are replaced by cmd/shops, and each loader leaves the other's rows alone.
-- source_url and cover_url are the shop's product page and cover; Open Library rows leave them empty and use
-- their id and cover_id instead.

ALTER TABLE works
    ADD COLUMN source text NOT NULL DEFAULT 'openlibrary' CHECK (source IN ('openlibrary', 'chapters')),
    ADD COLUMN source_url text,
    ADD COLUMN cover_url text;
