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

Finds the 50 books closest in meaning to the query (`-candidates`), then re-orders them with a re-ranker
that reads the query and each book together, and prints the best 10 (`-n`). Optional filters on first
publication year, page count and original language; `-json` for machine-readable output. Only works with a
vector are searched.

`-retrieval` (or `RETRIEVAL`) chooses how the candidates are found:
- `keyword-vectors-first` (default): the books whose keyword vector is nearest to the query's, plus, among the books
  whose description vector is nearest, those without a keyword vector, merged by similarity. A keyword vector embeds
  the keywords an LLM extracted from the book's description (places, period, themes, mood, type of protagonist; see
  [docs/schema.md](docs/schema.md)). Books without keywords stay findable, and on a database without keyword vectors
  it works like `vector`.
- `vector`: the nearest description vectors.
- `keyword-vectors`: the nearest keyword vectors only.

The query is embedded as typed in every case. On 70 recommendation queries rated by an LLM, keyword vectors put more
relevant books in the top 10 than description vectors (68% vs 59% before re-ranking), and re-ranking their top 50
brought it to 81% (see [Benchmark](#benchmark)).

The re-ranker is [Voyage AI](https://docs.voyageai.com/docs/reranker)'s `rerank-3` when `VOYAGE_API_KEY` is set
(`-voyage-model` or `VOYAGE_MODEL` to change the model), as on Azure. Without a key, it is the local
`gte-reranker-modernbert-base` in the `reranker` service of `docker-compose.yml` (`-reranker` or `RERANKER_URL`),
which adds about 3 s per search on a 16-core CPU. `-no-rerank` skips re-ranking and keeps the retrieval order.
With Voyage's free tier (10,000 tokens per minute), re-ranking 50 candidates allows about one search per minute.

For Docker, put the key in a `.env` file next to `docker-compose.yml` (git-ignored):

```sh
VOYAGE_API_KEY=pa-...
```

## Benchmark

```sh
go run ./cmd/bench -o bench.md
```

Evaluates the search on 70 queries in which a reader asks for a kind of book (`internal/bench/queries.json`): 10 per
category (plot, vibe, new experience, discovering something, like a book or author, character-driven, practical
constraints), in lengths S to XL. It runs the search as configured (`-retrieval`, `-candidates`, the re-ranker, with
the same flags and environment as `cmd/search`) in two stages:
- retrieval: the top `-candidates` (50), the pool the re-ranker orders, judged as a whole;
- re-ranking: the final top 10, which is what a reader sees.

An LLM rates the top 100 of each query (`-depth`) as a recommendation for that reader: 0 to 3 overall, and 0 to 2
on each dimension the query asks about (plot, mood, place, era, subject, character, style, constraints). The ranks
below the pool tell a retrieval miss from a gap in the catalogue. The report gives:
- relevant works (rated 2 or 3) in the pool;
- the final top 10: mean rating, shares rated 2–3, 3 and 0, nDCG@10, queries with at least 5 and 8 relevant works;
- how many of the relevant works the pool could put in the top 10 the re-ranker puts there, and the effect of
  re-ranking;
- the same by category, query length and dimension;
- the 10 worst queries with their top 5 and a likely cause: ranking, retrieval or a possible catalogue gap.

The LLM is set by `LLM_PROVIDER`, `LLM_URL`, `LLM_MODEL` and `LLM_API_KEY`, as in `internal/llm`: `azure`, `ollama` or
`copilot`. For Copilot (e.g. `LLM_MODEL=claude-haiku-5.5`, billed to the signed-in GitHub account, or the one of a
token in `LLM_API_KEY`), build the Copilot runtime into the command first with `go tool bundler --output cmd/bench`,
or set `COPILOT_CLI_PATH`.

Ratings are cached in `bench-judgments.jsonl` (`-judge-cache`) by model, rubric version, query and book text, so a
rerun only rates new results; `-dry-run` counts them. `-concurrency` (4) and `-per-minute` (60) pace the LLM. A first
run makes up to 7,000 ratings (70 × 100), about 2 hours at 60 a minute.

The re-ranker's scores are cached in `bench-rerank-scores.jsonl` (`-rerank-cache`). On Voyage's free tier, pace the
calls with `-rerank-chunk 20 -rerank-per-minute 3 -rerank-tokens-per-minute 8000`; the waits don't count in the
re-rank times. The report is written once the ratings are in, in retrieval order, then again after re-ranking.

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
