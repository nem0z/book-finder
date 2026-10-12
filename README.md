# book-finder

Describe the book you want in plain language and get recommendations grounded in real catalogue data.
The AI only ranks and explains books retrieved from our own database, so it can't invent titles or facts.

Status: proof of concept, in progress. See [docs/schema.md](docs/schema.md) for the data model and indexes.

## Requirements

- Go 1.27+
- Postgres 17 with [pgvector](https://github.com/pgvector/pgvector) 0.8+ (the provided `docker-compose.yml` runs one)

## Start Postgres

```sh
docker compose up -d
```

This starts Postgres 17 with pgvector on `localhost:5432` (user, password and database: `bookfinder`), with
settings tuned for bulk loading. Data is kept in the `pgdata` volume; `docker compose down -v` deletes it.

## Create the schema

```sh
go run ./cmd/migrate
```

Migrations live in `internal/db/migrations` and are applied in file-name order, once each. Set `DATABASE_URL`
to use another database (default `postgres://bookfinder:bookfinder@localhost:5432/bookfinder?sslmode=disable`).

## Development

```sh
go test ./...
```

Database tests need a disposable database and must run one package at a time:

```sh
BOOKFINDER_TEST_DATABASE_URL=postgres://bookfinder:bookfinder@localhost:5432/bookfinder?sslmode=disable go test -p 1 ./...
```

CI runs them against Postgres 17 + pgvector on every push.
