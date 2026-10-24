# book-finder

Describe the book you want in plain language and get recommendations grounded in real catalogue data.
The AI only ranks and explains books retrieved from our own database, so it can't invent titles or facts.

Status: proof of concept, in progress. See [docs/schema.md](docs/schema.md) for the data model and indexes.

## Requirements

- Go 1.27+
- Postgres 17 with [pgvector](https://github.com/pgvector/pgvector) 0.8+, [Ollama](https://ollama.com) and [Text Embeddings Inference](https://github.com/huggingface/text-embeddings-inference) (the provided `docker-compose.yml` runs all three)

## Start Postgres

```sh
docker compose up -d
```

This starts Postgres 17 with pgvector on `localhost:5432` (user, password and database: `bookfinder`), with
settings tuned for bulk loading. Data is kept in the `pgdata` volume; `docker compose down -v` deletes it.

## The models

Semantic search uses [EmbeddingGemma](https://ai.google.dev/gemma/docs/embeddinggemma), served by Ollama on
`localhost:11434`, and a re-ranker: Voyage AI's API when `VOYAGE_API_KEY` is set (see [Search](#search)), otherwise a
local model served by Text Embeddings Inference on `localhost:8081`. Both local models are built
into their images (`deploy/ollama`, `deploy/reranker`), so the first `docker compose up -d` builds them, downloading
about 2 GB once; nothing is downloaded when a container starts.

All ports are published on `localhost` only.

## Create the schema

```sh
go run ./cmd/migrate
```

Migrations live in `internal/db/migrations` and are applied in file-name order, once each. Set `DATABASE_URL`
to use another database (default `postgres://bookfinder:bookfinder@localhost:5432/bookfinder?sslmode=disable`).

## Load the catalogue

Download the works, editions, authors, redirects and ratings dumps from
[openlibrary.org/developers/dumps](https://openlibrary.org/developers/dumps) (about 17 GB in total; `data/` is
git-ignored), then:

```sh
go run ./cmd/ingest \
  -works data/ol_dump_works_2026-09-30.txt.gz \
  -editions data/ol_dump_editions_2026-09-30.txt.gz \
  -authors data/ol_dump_authors_2026-09-30.txt.gz \
  -redirects data/ol_dump_redirects_2026-09-30.txt.gz \
  -ratings data/ol_dump_ratings_2026-09-30.txt.gz
```

It applies the migrations, then rebuilds the catalogue from scratch: the existing `works`, `authors` and
`work_authors` rows are replaced. See [docs/schema.md](docs/schema.md) for what is kept.

## Compute the vectors

```sh
go run ./cmd/embed -limit 10000
```

Embeds the 10,000 most-rated works with a clean description (at least 300 bytes, no links or HTML) through
Ollama. Without `-limit` it embeds every eligible work, which takes about 11 hours on a 16-core CPU. It only
computes missing or outdated vectors, so it can be stopped with Ctrl+C and run again.

## Extract keywords

```sh
go run ./cmd/enrich -limit 300 -per-minute 30
```

An LLM reads the title, authors and description of the works with a vector and extracts keywords: places, period,
themes, tropes, mood and type of protagonist, with common synonyms. It also returns structured setting facts (places,
the years the story takes place) and themes. They're stored in `work_keywords` (see [docs/schema.md](docs/schema.md))
for keyword search. It works most-rated first and only extracts works whose keywords are missing or outdated (a new
model, prompt or description), so it can be stopped with Ctrl+C and run again.

- `-ids OL1W,OL2W` or `-ids @file` (one ID per line) limits the run to some works.
- `-concurrency` sets how many requests run at once (default 4).
- `-per-minute` caps the requests per minute, to leave room for other users of the same deployment (about 700 tokens
  per request).
- `-per-request 5` asks about 5 books per request. Each book has its ID in the request and must have it in the answer. Books
  the answer misses are asked again one by one, and so is the whole group when an ID comes back twice or was not asked
  about. It sends the instructions once per request, and gets around per-request rate limits: with GitHub Copilot
  (claude-haiku-5.5), 1 book per request tops out at about 400 requests per minute, while 96 concurrent requests of 5
  books extract about 1,800 books per minute. On 60 test books, packed keywords matched single-book ones a little less
  than a second single-book run did, with no words taken from the other books in the request. 10 books per request
  were riskier.

The LLM is set with environment variables:
- `LLM_PROVIDER`: `azure` (default) or `ollama`.
- `LLM_URL`: e.g. `https://<resource>.openai.azure.com/openai/v1`, or `http://localhost:11434`.
- `LLM_MODEL`: the deployment name on Azure.
- `LLM_API_KEY`.

Keep the key in the git-ignored `.env`.

## Search

```sh
go run ./cmd/search "a melancholic story about memory and loss"
go run ./cmd/search -max-year 1980 -lang jpn -n 5 "a lonely man drifting through life"
```

Finds the 30 books whose vector is closest to the query's (`-candidates`), then re-orders them with a re-ranker
that reads the query and each book together, and prints the best 10 (`-n`). Optional filters on first
publication year, page count and original language; `-json` for machine-readable output. Only works with a
vector are searched.

The re-ranker is [Voyage AI](https://docs.voyageai.com/docs/reranker)'s `rerank-3` when `VOYAGE_API_KEY` is set
(`-voyage-model` or `VOYAGE_MODEL` to change the model), as on Azure. Without a key, it is the local
`gte-reranker-modernbert-base` in the `reranker` service of `docker-compose.yml` (`-reranker` or `RERANKER_URL`),
which adds about 3 s per search on a 16-core CPU. `-no-rerank` skips re-ranking and ranks by vector similarity
only.

For Docker, put the key in a `.env` file next to `docker-compose.yml` (git-ignored):

```sh
VOYAGE_API_KEY=pa-...
```

## API

```sh
go run ./cmd/api
curl "http://localhost:8080/search?q=a+young+bride+haunted+by+her+husband's+dead+first+wife"
```

Or in Docker, with the other services: `docker compose up -d api` (rebuild after code changes with
`docker compose up -d --build api`).

`GET /search?q=<description>` returns the 10 best matches (vector search, then re-ranking) as JSON: title,
authors, year, pages, full description, tags, cover URL and Open Library link. Flags: `-addr`
(default `localhost:8080`), `-no-rerank`, and the same `-db`, `-ollama`, `-reranker` and `-voyage-model` as
`cmd/search`.

## Web app

A React page with a search box and the results below: cover, title, authors, year, pages, description
(long ones cut, with "See more") and tags. It needs Node 24+ and the API running (`go run ./cmd/api`):

```sh
cd web
npm install
npm run dev
```

Then open http://localhost:5173. In development, Vite forwards `/search` to the API on `localhost:8080`.

Or run everything in Docker: `docker compose up -d --build`, then open http://localhost:3000. The `web` service
serves the built page with nginx and forwards `/search` to the `api` service.

## Azure

See [docs/azure.md](docs/azure.md) for the cloud setup (Container Apps and managed PostgreSQL in France Central).

## Development

```sh
go test ./...
```

Database tests need a disposable database (they empty the catalogue, so don't point them at the loaded one) and must run one package at a time.
With the compose database, create one once with
`docker compose exec db psql -U bookfinder -c "CREATE DATABASE bookfinder_test"`, then:

```sh
BOOKFINDER_TEST_DATABASE_URL=postgres://bookfinder:bookfinder@localhost:5432/bookfinder_test?sslmode=disable go test -p 1 ./...
```

CI runs them against Postgres 17 + pgvector on every push.
