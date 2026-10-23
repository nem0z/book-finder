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

### Query understanding (optional)

With an LLM configured, the search first has it read the query. It pulls out the publication years and page
counts the query states ("set in modern Amsterdam (2020+)" means first published in 2020 or later; "under 200
pages"), and rewrites the rest as a short book blurb without the conversational part ("I'm looking for a book
that..."). The blurb is embedded in place of the query. The extracted filters apply unless the matching `-min-year`/
`-max-year` or `-min-pages`/`-max-pages` flags are given, and the re-ranker still reads the original query.
`cmd/search` prints what it searched for. If the LLM fails or takes longer than `-llm-timeout` (default 5s), the
search goes on with the query as is. `-no-llm` turns it off.

`LLM_PROVIDER` (`-llm`) picks the model's provider; `LLM_MODEL` (`-llm-model`) the model. In `.env` or the
environment:

| Provider | Settings | Billed to |
|---|---|---|
| `copilot` | `LLM_MODEL=claude-haiku-5.5`; `LLM_API_KEY` = a GitHub token with Copilot access, or empty to use the account signed in to Copilot CLI or `gh` | the GitHub account's AI credits |
| `azure` | `LLM_URL=https://<resource>.openai.azure.com/openai/v1`, `LLM_MODEL=<deployment>`, `LLM_API_KEY=<key>` | the Azure subscription |
| `ollama` | `LLM_URL=http://localhost:11434`, `LLM_MODEL=qwen2.5:3b`, `LLM_TIMEOUT=60s` | nothing (local) |

`copilot` goes through the [Copilot SDK](https://github.com/github/copilot-sdk), which runs the Copilot runtime as
a child process. The Docker image has it built in. For `go run`, build it into the command once with
`go tool bundler` in its directory (`cd cmd/search && go tool bundler`; the files are git-ignored), or point
`COPILOT_CLI_PATH` to a `copilot-runtime` executable. The bundler also marks `github.com/klauspost/compress` as a
direct dependency in `go.mod`, which its generated files import; leave that change out of commits
(`git checkout go.mod`). With `claude-haiku-5.5`, reading a query takes 2 to 3 s.

For `ollama`, the official `ollama/ollama` image can serve a small chat model (`ollama pull qwen2.5:3b`); the
`ollama` service of `docker-compose.yml` only has the embedding model. Small local models follow the
instructions less well and take 5 to 15 s on CPU.

## API

```sh
go run ./cmd/api
curl "http://localhost:8080/search?q=a+young+bride+haunted+by+her+husband's+dead+first+wife"
```

Or in Docker, with the other services: `docker compose up -d api` (rebuild after code changes with
`docker compose up -d --build api`).

`GET /search?q=<description>` returns the 10 best matches (vector search, then re-ranking) as JSON: title,
authors, year, pages, full description, tags, cover URL and Open Library link. When an LLM read the query (see
[Query understanding](#query-understanding-optional)), `interpretation` gives the description it searched for and
the filters it applied (`min_year`, `max_year`, `min_pages`, `max_pages`); the web page shows them above the
results. It is absent when the query was searched as typed. Flags: `-addr`
(default `localhost:8080`), `-no-rerank`, and the same `-db`, `-ollama`, `-reranker`, `-voyage-model`, `-llm`,
`-llm-url`, `-llm-model` and `-llm-timeout` as `cmd/search`.

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
