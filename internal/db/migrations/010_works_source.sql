-- Where a work comes from. Open Library works are rebuilt by cmd/ingest; bookshop works (one row per book the
-- shop lists, id '<source>:<isbn13>') are replaced by cmd/shops, and each loader leaves the other's rows alone.
-- A bookshop's source is its ID in the shops registry (internal/shops), so adding a shop needs no migration.
-- source_url, cover_url and in_stock are the shop's product page, cover and stock; Open Library rows leave them
-- empty and use their id and cover_id instead.

ALTER TABLE works
    ADD COLUMN source text NOT NULL DEFAULT 'openlibrary',
    ADD COLUMN source_url text,
    ADD COLUMN cover_url text,
    ADD COLUMN in_stock boolean;
