# book-finder

Describe the book you want in plain language and get recommendations grounded in real catalogue data.
The AI only ranks and explains books retrieved from our own database, so it can't invent titles or facts.

Status: proof of concept. So far: the database, loading the [Open Library dumps](https://openlibrary.org/developers/dumps),
and semantic search with a local embedding model.
See [docs/schema.md](docs/schema.md) for the data model, indexes and load process.

## Requirements

- Go 1.27+
- Postgres 17 with [pgvector](https://github.com/pgvector/pgvector) 0.8+ (the provided `docker-compose.yml` runs one),
  ~20 GB free disk for loading
- [Ollama](https://ollama.com) for embeddings, with the model pulled once (~620 MB): `ollama pull embeddinggemma`

## 1. Download the dumps

From https://openlibrary.org/developers/dumps, into a folder such as `./data` (keep the original file names):

| File | Size | Needed |
|---|---|---|
| `ol_dump_works_*.txt.gz` | ~3.8 GB | yes |
| `ol_dump_editions_*.txt.gz` | ~11.8 GB | yes |
| `ol_dump_authors_*.txt.gz` | ~0.7 GB | yes |
| `ol_dump_redirects_*.txt.gz` | ~50 MB | recommended (resolves merged records) |
| `ol_dump_ratings_*.txt.gz` | ~5 MB | optional |
| `ol_dump_reading-log_*.txt.gz` | ~65 MB | optional (popularity) |

No need to decompress them.

## 2. Start Postgres

```sh
docker compose up -d
```

Any Postgres 17 works; set `DATABASE_URL` (default `postgres://bookfinder:bookfinder@localhost:5432/bookfinder?sslmode=disable`).

## 3. Load

```sh
go run ./cmd/ingest -dir ./data
```

This applies the schema, then rebuilds the catalogue from the dumps and prints counts. Re-running replaces the
data, so it can be repeated with each monthly dump. Files can also be passed individually (`-works`, `-editions`,
`-authors`, `-redirects`, `-ratings`, `-reading-log`). To only create the schema: `go run ./cmd/migrate`.

## 4. Embed

```sh
go run ./cmd/embed              # all works with a description (hours on a laptop CPU)
go run ./cmd/embed -limit 1000  # quick trial: the 1,000 most-read works
```

Progress logs show works per second and an ETA. It can be stopped and re-run at any time: finished works are
kept, and after a monthly reload only works whose text changed are re-embedded. Use `-model` / `EMBED_MODEL`
and `-ollama` / `OLLAMA_URL` to change the model or server (the schema expects 768 dimensions).

## 5. Search

```sh
go run ./cmd/search "a melancholic story about memory and loss in post-war Japan"
go run ./cmd/search -max-year 1970 -max-pages 250 "a slow, quiet novel about a lonely man"
```

Filters: `-min-year`, `-max-year`, `-min-pages`, `-max-pages`, `-lang` (original language, e.g. `jpn`), `-n`, `-json`.

## Development

```sh
go test ./...
```

The database tests need a disposable database (they truncate the catalogue) and must run one package at a time:

```sh
BOOKFINDER_TEST_DATABASE_URL=postgres://bookfinder:bookfinder@localhost:5432/bookfinder?sslmode=disable go test -p 1 ./...
```

CI runs them against Postgres 17 + pgvector on every push. They use a stand-in embedder, so no model download is needed.
