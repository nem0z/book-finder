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

It applies the migrations, then rebuilds the Open Library catalogue from scratch: the existing Open Library `works`,
and all `authors` and `work_authors` rows, are replaced. Bookshop works (`source` other than `openlibrary`) are kept.
See [docs/schema.md](docs/schema.md) for what is kept.

## Index the catalogue

```sh
LLM_PROVIDER=copilot LLM_MODEL=claude-haiku-5.5 go run ./cmd/index -dry-run
LLM_PROVIDER=copilot LLM_MODEL=claude-haiku-5.5 go run ./cmd/index
```

Makes the catalogue searchable, in one step after `cmd/ingest`. For each eligible work (a description of at least
300 characters, without links or HTML), most-rated first, an LLM extracts keywords from its title, authors and
description: places, period, themes, tropes, mood, type of protagonist (see [docs/schema.md](docs/schema.md)).
Ollama then embeds them into the work's keyword vector, which search compares with the query's.

Requests for several works (`-per-request`, 5) run at once (`-concurrency`, 4), and each work's keywords are
embedded as soon as they arrive (`-embed-batch` texts per Ollama call), so the step takes about as long as the
slower of the two. Ollama embeds about 500 works a minute on a 16-core CPU; at the default 60 requests a minute
(`-per-minute`), the LLM extracts up to 300. The whole catalogue (about 160,000 works) takes about 9 hours, and its
extraction was estimated at about 2,800 Copilot AI credits.

The step only does what is missing or stale. A work's keywords are extracted again when its title, authors or
description change, or with another LLM model or prompt; its vector is computed again when its keywords or the
embedding model change. So it can be stopped with Ctrl+C and run again, and after a reload it only indexes new or
edited works. `-dry-run` counts what it would do; `-limit` indexes the N most-rated works only. A full run ends
by deleting the keywords and vectors of works no longer in the catalogue.

A work whose extraction fails 3 times (`-attempts`; the last asks for it alone) is logged and left for the next run.
On a rate limit (429), the step waits as asked, or longer each time, and halves its concurrency for a while
(`-retries`, `-timeout`). The Copilot runtime is restarted every 30 minutes (`-restart-every`), as it slows down
over long runs, and after 5 failures in a row (`-restart-after`).

The LLM is set by `LLM_PROVIDER` (`azure`, `ollama` or `copilot`), `LLM_URL`, `LLM_MODEL` and `LLM_API_KEY`. The
model is part of the keywords' hash: another model extracts every work again. Copilot is billed to the signed-in
GitHub account, or to the account of a token in `LLM_API_KEY`. Build the Copilot runtime into the command first
with `go tool bundler --output cmd/index`, or set `COPILOT_CLI_PATH`.

## Search

```sh
go run ./cmd/search "a melancholic story about memory and loss"
go run ./cmd/search -max-year 1980 -lang jpn -n 5 "a lonely man drifting through life"
```

Finds the 50 books whose keyword vector is nearest to the query's (`-candidates`), then re-orders them with a
re-ranker that reads the query and each book together, and prints the best 10 (`-n`). A keyword vector embeds the
keywords an LLM extracted from the book's description (places, period, themes, mood, type of protagonist; see
[Index the catalogue](#index-the-catalogue)). The query is embedded as typed. Optional filters on first publication
year, page count and original language; `-json` for machine-readable output. Only indexed works are searched.

On 70 recommendation queries rated by an LLM, keyword vectors put more relevant books in the top 10 than vectors of
the descriptions did (68% vs 59% before re-ranking), and re-ranking their top 50 brought it to 81% (see
[Benchmark](#benchmark)).

The re-ranker is [Voyage AI](https://docs.voyageai.com/docs/reranker)'s `rerank-3` when `VOYAGE_API_KEY` is set
(`-voyage-model` or `VOYAGE_MODEL` to change the model), as on Azure. Without a key, it is the local
`gte-reranker-modernbert-base` in the `reranker` service of `docker-compose.yml` (`-reranker` or `RERANKER_URL`),
which adds about 3 s per search on a 16-core CPU. `-no-rerank` skips re-ranking and keeps the retrieval order.
With Voyage's free tier (10,000 tokens per minute), re-ranking 50 candidates allows about one search per minute.

For Docker, put the key in a `.env` file next to `docker-compose.yml` (git-ignored):

```sh
VOYAGE_API_KEY=pa-...
```

### Query understanding (optional)

With an LLM configured, the search first has it read the query. It pulls out the publication years and page
counts the query states ("set in modern Amsterdam (2020+)" means first published in 2020 or later; "under 200
pages"), and rewrites the query as the keywords a matching book would have, in the shape of the books' own
keywords: places with their synonyms, era, themes, tropes, mood, type of protagonist. Retrieval embeds those keywords
in place of the query. The extracted filters apply unless the matching `-min-year`/`-max-year` or
`-min-pages`/`-max-pages` flags are given, and the re-ranker still reads the original query. These choices come from
the benchmark in [docs/benchmarks.md](docs/benchmarks.md#query-understanding-what-retrieval-and-the-re-ranker-read-2026-10-31).
`cmd/search` prints what it searched for. If the LLM fails or takes longer than `-llm-timeout` (default 8s), the
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
(`git checkout go.mod`). With `claude-haiku-5.5`, reading a query takes 3 to 6 s.

For `ollama`, the official `ollama/ollama` image can serve a small chat model (`ollama pull qwen2.5:3b`); the
`ollama` service of `docker-compose.yml` only has the embedding model. Small local models follow the
instructions less well and take 5 to 15 s on CPU.

## Benchmark

The results so far, and the decisions they led to (keyword vectors, Voyage re-ranking, a pool of 50, what query
understanding feeds retrieval and the re-ranker), are in [docs/benchmarks.md](docs/benchmarks.md).

```sh
go run ./cmd/bench -o bench.md
```

Evaluates the search on 70 queries in which a reader asks for a kind of book (`internal/bench/queries.json`): 10 per
category (plot, vibe, new experience, discovering something, like a book or author, character-driven, practical
constraints), in lengths S to XL. It runs the search as configured (`-candidates`, the re-ranker, with
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
authors, year, pages, full description, tags, cover URL and Open Library link. When an LLM read the query (see
[Query understanding](#query-understanding-optional)), `interpretation` gives the keywords it searched for and
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
