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
| `work_embeddings` | work with a description | model, text hash, 768-dimension vector |

Keys are Open Library IDs without the path prefix (`OL45804W`, `OL7353617M`, `OL23919A`), so any row maps back to
`https://openlibrary.org/works/<id>`.

### Where each `works` column comes from

| Column | Source |
|---|---|
| `title`, `subtitle`, `subjects`, `subject_places/people/times` | work record |
| `description` | work description; else the longest English edition description (`description_source` says which) |
| `first_publish_year` | year parsed from the work's `first_publish_date`; else the earliest edition year. Can be wrong for badly merged works |
| `original_language` | most frequent `translated_from` across editions; else the language of the earliest dated edition (heuristic) |
| `page_count` | median `number_of_pages` of English editions (10–5000) |
| `genres` | union of English edition `genres` (max 10) |
| `author_names` | authors dump, in work author order (denormalised for search and display) |
| `cover_id` | first work cover, else first English edition cover. Image: `https://covers.openlibrary.org/b/id/<cover_id>-M.jpg` |
| `edition_count`, `english_edition_count` | editions dump |
| `rating_avg`, `rating_count` | ratings dump |
| `want_to_read`, `currently_reading`, `already_read` | reading-log dump (better popularity signal than edition count) |

Merged records are resolved with the redirects dump, so ratings, reading-log entries and editions that point at an old
work ID still count for the surviving work.

## Indexes

| Purpose | Index |
|---|---|
| Keyword search | GIN on `works.search_tsv`: title + author names (weight A), subjects + genres (B), description (C) |
| Fuzzy title / author lookup ("like *X*") | trigram GIN on `lower(works.title)` and `lower(authors.name)` |
| Hard filters | B-tree on `first_publish_year`, `page_count`, `original_language`; GIN on `genres`, `subjects` |
| Popularity ordering | B-tree on `want_to_read + currently_reading + already_read` |
| Joins / lookups | `work_authors(author_id)`, `editions(work_id)`, GIN on `editions.isbn_13` |

Semantic (meaning-based) search uses `work_embeddings`: one 768-dimension vector per work from
[EmbeddingGemma](https://huggingface.co/google/embeddinggemma-300m), run locally with Ollama.

| Purpose | Index |
|---|---|
| Find works whose meaning is closest to the query | HNSW on `work_embeddings.embedding` (`halfvec`, cosine) |

- `halfvec` (16-bit floats) halves storage: ~1 GB table + index for ~300k works.
- Query-time recall is set with `hnsw.ef_search` (200). Filters (year, pages, language) are applied during the
  index walk using pgvector's iterative scans (`hnsw.iterative_scan = relaxed_order`), so selective filters still
  return a full result list.
- Embeddings are kept across catalogue rebuilds (no foreign key to `works`). Each row stores the model and a hash of
  the embedded text; `cmd/embed` only re-embeds works whose text or model changed and prunes deleted works.
- Embedded text per work: title (+ subtitle), authors, up to 15 genres/subjects, and the description capped at 4,000
  characters (~1,000 tokens, within EmbeddingGemma's 2,048-token window), using EmbeddingGemma's prompt formats
  (`title: … | text: …` for books, `task: search result | query: …` for queries).
- Only works with a description are embedded by default; title and subjects alone carry little meaning.

## Loading

`cmd/ingest` does a full rebuild, streaming each gzip file once (nothing is decompressed to disk):

1. **redirects**: map of merged work/author IDs.
2. **works**: byte prefilter on "iction", then JSON decode on all CPUs; fiction works go to an unlogged staging table
   via `COPY`, and their IDs stay in memory.
3. **editions**: the work key is found without decoding JSON, so only editions of candidate works are decoded. Per-work
   aggregates (English count, pages, original language, genres, fallback description) stay in memory; English editions
   go to staging via `COPY`.
4. **authors**: only those referenced by candidate works.
5. **ratings**, **reading log**: aggregated per work.
6. **finalize**: one `INSERT … SELECT` per final table (works without an English edition are dropped), unreferenced
   authors removed, secondary indexes rebuilt, `ANALYZE`.

Secondary indexes are dropped before loading and rebuilt at the end, which is much faster than maintaining them row by
row. Peak memory is roughly the per-work aggregates of the ~1.7M fiction candidates (a few hundred MB).
