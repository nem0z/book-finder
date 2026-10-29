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
| `work_keywords` | work with LLM keywords | keywords, setting places, setting era (`era_from`/`era_to`, label), themes, protagonist type; model, hash of the extracted text |
| `work_keyword_embeddings` | work with LLM keywords | EmbeddingGemma vector of the keywords (`halfvec(768)`), model, hash of the embedded text |

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
| Semantic search | HNSW (cosine) on `work_keyword_embeddings.embedding` |

`work_keywords` and `work_keyword_embeddings` are filled by `cmd/index` after each load: an LLM extracts the keywords
from each work's title, authors and description, which takes hours and costs money, and EmbeddingGemma embeds them
into one vector per work, which search compares with the query's vector. Only eligible works are indexed: a
description of at least 300 characters, without links or HTML. The setting era is when the story takes place, not
when the book was published.

Neither table has a foreign key to `works`, and the vectors' index is not rebuilt by the loader: they survive
catalogue reloads, and only works whose text (or keywords) changed are indexed again.

Migrations that removed what search no longer uses:
- `006`, a full-text index on the keywords for a keyword search that was not kept, has no file; `008` drops that
  index, where it exists, and the full-text column of `work_keywords`.
- `009` drops `work_embeddings`, the vectors of the descriptions, which keyword vectors replaced. It stops with an
  error when a work with a description vector has no keyword vector: run `cmd/index` first.

The secondary indexes live in their own migration (`003_indexes.sql`) so the loader can drop them before a bulk load
and rebuild them afterwards, which is much faster than maintaining them row by row.
