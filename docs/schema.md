# Data model and indexing

The catalogue holds **English-language fiction** from the [Open Library dumps](https://openlibrary.org/developers/dumps):
works with a fiction subject, at least one English edition, and a description (the work's own or an English
edition's). A fiction subject contains "fiction" in any case (`Fiction`, `Juvenile fiction`, `Fiction, Romance, General`)
and is not a non-fiction one (`Juvenile nonfiction`) or about fiction (`American fiction, History and criticism`).
Works without a description are left out until another description source is added.

Years come from Open Library's free-text dates: a plain number (`850`), else the first 4-digit number in the text
(`June 13, 2005` → 2005).

Original language is the most frequent `translated_from` across the work's editions, else the language of its earliest
dated edition (`und`, `mul` and `zxx` are ignored). Checked against Wikidata on the 2026-09-30 dump, `translated_from` is
right in 94% of the cases where the two disagree; without it, the earliest edition is right 96% of the time when it is
English and 67% when it is in another language (about 1% of works).

## Tables

| Table | One row per | Main columns |
|---|---|---|
| `works` | book (all editions together) | title, description, first publish year, original language, page count, ISBN-13, subjects, genres, author names, ratings and reading-log counts, `search_tsv` |
| `authors` | author of a kept work | name, birth/death date, Wikidata ID |
| `work_authors` | work–author link | position (author order) |
| `ingest_runs` | load run | source files, counts |
| `work_embeddings` | work with a vector | EmbeddingGemma vector (`halfvec(768)`), model, hash of the embedded text |
| `work_keywords` | work with LLM keywords | keywords, setting places, setting era (`era_from`/`era_to`, label), themes, protagonist type, all of them as `keywords_tsv`; model, hash of the extracted text |

Keys are Open Library IDs without the path prefix (`OL45804W`, `OL23919A`), so any row maps back to
`https://openlibrary.org/works/<id>`.

Editions are not stored. When a work has no description, it takes the longest description among its English editions.
For page count, ISBN-13 and cover, the most complete English edition fills in what the work lacks: it prefers an edition
with a page count, then an ISBN-13, then a cover; ties go to the most recent edition.

## Indexes

| Purpose | Index |
|---|---|
| Keyword search | GIN on `works.search_tsv`: title + author names (weight A), subjects + genres (B), description (C) |
| Fuzzy title / author lookup ("like *X*") | trigram GIN on `lower(works.title)` and `lower(authors.name)` |
| Hard filters | B-tree on `first_publish_year`, `page_count`, `original_language`; GIN on `genres`, `subjects` |
| Popularity ordering | B-tree on `want_to_read + currently_reading + already_read + rating_count` |
| Joins / lookups | `work_authors(author_id)`, `works(isbn_13)` |
| Semantic search | HNSW (cosine) on `work_embeddings.embedding` |

`work_embeddings` has no foreign key to `works` and its index is not rebuilt by the loader: vectors take hours to
compute, so they survive catalogue reloads, and only works whose embedded text changed are computed again.

`work_keywords` works the same way: an LLM extracts the keywords from each work's title, authors and description
(`internal/enrich`), which takes hours and costs money. Only works with a vector get keywords, so keyword and vector
search cover the same catalogue. The setting era is when the story takes place, not when the book was published.

The secondary indexes live in their own migration (`003_indexes.sql`) so the loader can drop them before a bulk load
and rebuild them afterwards, which is much faster than maintaining them row by row.
