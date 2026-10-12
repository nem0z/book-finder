# book-finder

Describe the book you want in plain language and get recommendations grounded in real catalogue data.
The AI only ranks and explains books retrieved from our own database, so it can't invent titles or facts.

Status: proof of concept. This first part covers the database and loading the [Open Library dumps](https://openlibrary.org/developers/dumps).
See [docs/schema.md](docs/schema.md) for the data model, indexes and load process.

## Requirements

- Go 1.27+
- Postgres 17 (the provided `docker-compose.yml` runs one with pgvector) with ~20 GB free disk for loading

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

## Development

```sh
go test ./...
```

The end-to-end loader test needs a disposable database (it truncates the catalogue):

```sh
BOOKFINDER_TEST_DATABASE_URL=postgres://bookfinder:bookfinder@localhost:5432/bookfinder?sslmode=disable go test ./...
```

CI runs it against Postgres 17 on every push.
