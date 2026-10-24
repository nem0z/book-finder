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

`go run ./cmd/embed -keywords` embeds each work's LLM keywords (see [Extract keywords](#extract-keywords)) instead,
into `work_keyword_embeddings`, for searching them by meaning (`cmd/search -retrieval keyword-vectors`). It takes
about 8 minutes per 4,000 works.

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
which adds about 3 s per search on a 16-core CPU. `-no-rerank` skips re-ranking and keeps the retrieval order.

`-retrieval` chooses how candidates are found, before re-ranking:
- `vector` (default): the nearest vectors.
- `keywords`: the works whose LLM keywords (`work_keywords`) best match the query's words.
- `fulltext`: the same keyword match on the title, authors, subjects and description.

Keyword retrieval ranks with BM25, so rare words count more than common ones. It only searches works with a vector,
so all three cover the same catalogue. For keyword retrieval, re-rank more candidates, e.g. `-candidates 100`.

For Docker, put the key in a `.env` file next to `docker-compose.yml` (git-ignored):

```sh
VOYAGE_API_KEY=pa-...
```

## Benchmark

```sh
go run ./cmd/bench -o bench.md
```

Runs 42 test queries (`internal/bench/queries.json`) through each search pipeline and writes a Markdown report. 41 of
the queries describe a known book without naming it. For those, the report shows whether the book is among the first
100 candidates, its rank after re-ranking, hit@1, hit@10, MRR and the mean times. One query is judged by hand, so
its top 10 is printed side by side for each pipeline.

Pipelines:
- `vector`: re-ranks the 30 nearest works, as `cmd/search` does.
- `vector-100`: re-ranks the 100 nearest.
- `keywords` and `keywords-100`: re-rank the 30 or 100 best matches on the LLM keywords.
- `fulltext-100`: re-ranks the 100 best matches on the title, authors, subjects and description.

Until every work has keywords, use `-sample`: every pipeline then searches only the works with keywords, and the report
says so. Results on a sample may not hold for the full catalogue.

Flags: `-o`, `-sample`, `-queries` (another query file), `-n`, `-no-rerank`, and the same `-db`, `-ollama`,
`-reranker` and `-voyage-model` as `cmd/search`. With the local re-ranker, a run takes about 30 minutes.

### Relevance of the retrieved books

```sh
go run ./cmd/bench -sample -judge -o relevance.md
```

With `-judge`, nothing is re-ranked. For each query, it takes the top 100 from each of these retrievals:
- `vector`: the description vectors nearest to the query's.
- `keywords`: BM25 on the LLM keywords.
- `keyword-vectors`: the vectors of the LLM keywords nearest to the query's (`cmd/embed -keywords` first).
- `keyword-vectors-llm`: the same, with the vector of the query's own keywords, which an LLM extracts like a book's.
  They're cached in `bench-query-keywords.jsonl` (`-query-keywords-cache`), and the LLM's answer time counts in the
  retrieval time.

An LLM rates every result:
- 0: not relevant.
- 1: weak match.
- 2: reasonable suggestion.
- 3: strong match.

The LLM reads the query and the book's title, authors, year and description; the rubric is in
`internal/bench/judge.go`. The report gives each method's mean rating and share of results rated 2 or more over the
top 10, 30 and 100, how many known targets each list contains, retrieval times, and, against the first method, how
many works each list shares with it and on how many queries it rates better or worse. An appendix gives the same per query.

A work in several lists is rated once. Ratings are cached in `bench-judgments.jsonl` (`-judge-cache`, git-ignored), keyed
by model, prompt version, query and book text, so a rerun only rates new pairs. `-concurrency` (default 4) and
`-per-minute` (default 60) pace the LLM calls. The LLM is set as for `cmd/enrich` (`LLM_PROVIDER`, `LLM_URL`,
`LLM_MODEL`, `LLM_API_KEY`). A full run makes up to 42 × 400 ratings, fewer when the lists overlap.

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
