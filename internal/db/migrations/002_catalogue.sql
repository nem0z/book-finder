-- Core catalogue: English-language fiction from the Open Library dumps.
-- Keys are Open Library IDs without their path prefix (e.g. OL45804W, OL23919A).
--
-- Editions are not stored. Only works with at least one English edition are kept, and the most
-- complete English edition (description, then page count, ISBN-13 and cover; ties go to the most
-- recent) fills in the fields a work lacks.

CREATE TABLE authors (
    id          text PRIMARY KEY,
    name        text NOT NULL,
    birth_date  text,
    death_date  text,
    wikidata_id text
);

-- One row per work (the book itself, independent of edition).
CREATE TABLE works (
    id                 text PRIMARY KEY,
    title              text NOT NULL,
    subtitle           text,
    -- Work description, or the chosen edition's description when the work has none.
    description        text,
    description_source text CHECK (description_source IN ('work', 'edition')),
    -- From the work's first_publish_date, else the earliest edition year. Can be wrong (bad merges).
    first_publish_year smallint,
    -- MARC code. Most common translated_from of English editions; else the language of the earliest dated edition.
    original_language  text,
    -- From the chosen edition.
    page_count         smallint,
    isbn_13            text,
    subjects           text[] NOT NULL DEFAULT '{}',
    subject_places     text[] NOT NULL DEFAULT '{}',
    subject_people     text[] NOT NULL DEFAULT '{}',
    subject_times      text[] NOT NULL DEFAULT '{}',
    -- Union of English edition genres (sparse, so not only the chosen edition's).
    genres             text[] NOT NULL DEFAULT '{}',
    -- Denormalised author names, in author order, for search and display.
    author_names       text[] NOT NULL DEFAULT '{}',
    -- Work cover, else the chosen edition's.
    cover_id           bigint,
    rating_avg         real,
    rating_count       integer NOT NULL DEFAULT 0,
    want_to_read       integer NOT NULL DEFAULT 0,
    currently_reading  integer NOT NULL DEFAULT 0,
    already_read       integer NOT NULL DEFAULT 0,
    -- Weighted full-text vector: title/authors (A), subjects/genres (B), description (C).
    search_tsv         tsvector
);

CREATE TABLE work_authors (
    work_id   text NOT NULL REFERENCES works (id) ON DELETE CASCADE,
    author_id text NOT NULL,
    position  smallint NOT NULL,
    PRIMARY KEY (work_id, author_id)
);

CREATE TABLE ingest_runs (
    id          bigserial PRIMARY KEY,
    started_at  timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    sources     jsonb NOT NULL,
    stats       jsonb NOT NULL
);
