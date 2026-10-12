-- Core catalogue: English-language fiction from the Open Library dumps.
-- Keys are Open Library IDs without their path prefix (e.g. OL45804W, OL1M, OL23919A).

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE authors (
    id          text PRIMARY KEY,
    name        text NOT NULL,
    birth_date  text,
    death_date  text,
    wikidata_id text
);

-- One row per work (the book itself, independent of edition).
CREATE TABLE works (
    id                    text PRIMARY KEY,
    title                 text NOT NULL,
    subtitle              text,
    -- Work description, or the longest English edition description when the work has none.
    description           text,
    description_source    text CHECK (description_source IN ('work', 'edition')),
    first_sentence        text,
    -- From the work's first_publish_date, else the earliest edition year. Can be wrong (bad merges).
    first_publish_year    smallint,
    -- MARC code. Most common edition translated_from; else the language of the earliest dated edition.
    original_language     text,
    -- Median page count of English editions.
    page_count            smallint,
    subjects              text[] NOT NULL DEFAULT '{}',
    subject_places        text[] NOT NULL DEFAULT '{}',
    subject_people        text[] NOT NULL DEFAULT '{}',
    subject_times         text[] NOT NULL DEFAULT '{}',
    -- Union of English edition genres.
    genres                text[] NOT NULL DEFAULT '{}',
    -- Denormalised author names, in author order, for search and display.
    author_names          text[] NOT NULL DEFAULT '{}',
    cover_id              bigint,
    edition_count         integer NOT NULL DEFAULT 0,
    english_edition_count integer NOT NULL DEFAULT 0,
    rating_avg            real,
    rating_count          integer NOT NULL DEFAULT 0,
    want_to_read          integer NOT NULL DEFAULT 0,
    currently_reading     integer NOT NULL DEFAULT 0,
    already_read          integer NOT NULL DEFAULT 0,
    -- Weighted full-text vector: title/authors (A), subjects/genres (B), description (C).
    search_tsv            tsvector
);

CREATE TABLE work_authors (
    work_id   text NOT NULL REFERENCES works (id) ON DELETE CASCADE,
    author_id text NOT NULL,
    position  smallint NOT NULL,
    PRIMARY KEY (work_id, author_id)
);

-- English editions only: ISBNs for links/lookups, formats and page counts.
CREATE TABLE editions (
    id              text PRIMARY KEY,
    work_id         text NOT NULL REFERENCES works (id) ON DELETE CASCADE,
    title           text NOT NULL,
    publish_year    smallint,
    number_of_pages smallint,
    physical_format text,
    isbn_13         text[] NOT NULL DEFAULT '{}',
    isbn_10         text[] NOT NULL DEFAULT '{}',
    cover_id        bigint
);

CREATE TABLE ingest_runs (
    id          bigserial PRIMARY KEY,
    started_at  timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    sources     jsonb NOT NULL,
    stats       jsonb NOT NULL
);
