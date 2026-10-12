# book-finder

Describe the book you want in plain language and get recommendations grounded in real catalogue data.
The AI only ranks and explains books retrieved from our own database, so it can't invent titles or facts.

Status: proof of concept, in progress.

## Requirements

- Postgres 17 with [pgvector](https://github.com/pgvector/pgvector) 0.8+ (the provided `docker-compose.yml` runs one)

## Start Postgres

```sh
docker compose up -d
```

This starts Postgres 17 with pgvector on `localhost:5432` (user, password and database: `bookfinder`), with
settings tuned for bulk loading. Data is kept in the `pgdata` volume; `docker compose down -v` deletes it.
