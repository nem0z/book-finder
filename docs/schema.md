# Data model and indexing

The catalogue holds **English-language fiction** from the [Open Library dumps](https://openlibrary.org/developers/dumps):
works whose subjects mention fiction (not "nonfiction") and that have at least one English edition.

## Tables

| Table | One row per | Main columns |
|---|---|---|
| `works` | book (all editions together) | title, description, first publish year, original language, median page count, subjects, genres, author names, popularity and ratings, `search_tsv` |
| `editions` | English edition of a kept work | ISBN-13/10, publish year, pages, format, cover |
| `authors` | author of a kept work | name, birth/death date, Wikidata ID |
| `work_authors` | work–author link | position (author order) |
| `ingest_runs` | load run | source files, counts |

Keys are Open Library IDs without the path prefix (`OL45804W`, `OL7353617M`, `OL23919A`), so any row maps back to
`https://openlibrary.org/works/<id>`.

## Indexes

| Purpose | Index |
|---|---|
| Keyword search | GIN on `works.search_tsv`: title + author names (weight A), subjects + genres (B), description (C) |
| Fuzzy title / author lookup ("like *X*") | trigram GIN on `lower(works.title)` and `lower(authors.name)` |
| Hard filters | B-tree on `first_publish_year`, `page_count`, `original_language`; GIN on `genres`, `subjects` |
| Popularity ordering | B-tree on `want_to_read + currently_reading + already_read` |
| Joins / lookups | `work_authors(author_id)`, `editions(work_id)`, GIN on `editions.isbn_13` |

The secondary indexes live in their own migration (`003_indexes.sql`) so the loader can drop them before a bulk load
and rebuild them afterwards, which is much faster than maintaining them row by row.
