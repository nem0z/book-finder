# Data model and indexing

The catalogue holds **English-language fiction** from the [Open Library dumps](https://openlibrary.org/developers/dumps):
works with a fiction subject, at least one English edition, and a description (the work's own or an English
edition's). A fiction subject contains "fiction" in any case (`Fiction`, `Juvenile fiction`, `Fiction, Romance, General`)
and is not a non-fiction one (`Juvenile nonfiction`) or about fiction (`American fiction, History and criticism`).
Works without a description are left out until another description source is added.

Years come from Open Library's free-text dates: a plain number (`850`), else the first 4-digit number in the text
(`June 13, 2005` → 2005).

## Tables

| Table | One row per | Main columns |
|---|---|---|
| `works` | book (all editions together) | title, description, first publish year, original language, page count, ISBN-13, subjects, genres, author names, ratings and reading-log counts, `search_tsv` |
| `authors` | author of a kept work | name, birth/death date, Wikidata ID |
| `work_authors` | work–author link | position (author order) |
| `ingest_runs` | load run | source files, counts |

Keys are Open Library IDs without the path prefix (`OL45804W`, `OL23919A`), so any row maps back to
`https://openlibrary.org/works/<id>`.

Editions are not stored. When a work has several English editions, the most complete one fills in what the work
lacks: it prefers an edition with a description, then one with a page count, an ISBN-13 and a cover; ties go to the
most recent edition.

## Indexes

| Purpose | Index |
|---|---|
| Keyword search | GIN on `works.search_tsv`: title + author names (weight A), subjects + genres (B), description (C) |
| Fuzzy title / author lookup ("like *X*") | trigram GIN on `lower(works.title)` and `lower(authors.name)` |
| Hard filters | B-tree on `first_publish_year`, `page_count`, `original_language`; GIN on `genres`, `subjects` |
| Popularity ordering | B-tree on `want_to_read + currently_reading + already_read + rating_count` |
| Joins / lookups | `work_authors(author_id)`, `works(isbn_13)` |

The secondary indexes live in their own migration (`003_indexes.sql`) so the loader can drop them before a bulk load
and rebuild them afterwards, which is much faster than maintaining them row by row.
