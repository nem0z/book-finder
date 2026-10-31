# Search benchmarks and the decisions they led to

The search pipeline was chosen by measuring it. This page keeps the numbers behind each decision. The tooling
lived in benchmark PRs that were closed without merging. `cmd/bench` on main runs the recommendation benchmark
(see the README).

## How the search is measured

- **Recommendation queries:** 70 queries in which a reader asks for a kind of book (`internal/bench/queries.json`).
  They come in 7 categories of 10: plot, vibe, new experience, discovering something, like a book or author,
  character-driven and practical constraints. Their lengths range from S to XL.
- **Judge:** an LLM (`claude-haiku-5.5` through the Copilot SDK, rubric `rec-1`) rates each result as a
  recommendation for that reader:
  - 0 to 3 overall;
  - 0 to 2 on each dimension the query asks about (plot, mood, place, era, subject, character, style,
    constraints).

  **Relevant** means rated 2 or 3. Works are always rated against the query as the reader typed it. There is one
  judge and no human calibration.
- **Catalogue:** the 159,479 English-language fiction works that have a description.
- **Two stages:**
  - **pool:** the candidates retrieval finds, which the re-ranker orders, judged as a whole;
  - **final top 10:** what the reader sees after re-ranking.
- **nDCG@10:** gain 2^rating − 1, against the best order of the works rated in the same run. It is comparable
  between runs with the same pool, not across pools.

Earlier benchmarks used 42 "known item" queries, each describing one book, judged 0 to 3 by the same model.

## Retrieval: keyword vectors instead of description vectors

Retrieval searched the vectors of the books' descriptions (EmbeddingGemma). It now searches the vectors of keywords
an LLM extracted from each description: places, era, themes, tropes, mood, protagonist (#55, #69).

| Benchmark | Description vectors | Keyword vectors |
|---|---|---|
| Known-item queries, whole catalogue, top 10 rated ≥2, no re-ranking (#51, 2026-10-24) | 50% | **60%** |
| Same, known targets in the top 10 (of 41) | 30 | **38** |
| Recommendation queries, top 10 rated ≥2, no re-ranking (#53, 2026-10-26) | 59% | **68%** |
| Same, nDCG@10 | 0.526 | **0.588** |

In #51, keyword vectors were better than description vectors on 31 queries and worse on 9 at the top 10. In #53
they were better on 44 and worse on 25. Description vectors were dropped in #69 (migration `009`).

## Re-ranking: Voyage rerank-3, not the local re-ranker

Measured on the known-item queries, on the keyword-vector top 100 (#52, 2026-10-24):

| Re-ranking of the top 30 | Top 10: mean rating / rated ≥2 | nDCG@10 | Time |
|---|---|---|---|
| none | 1.86 / 60% | 0.794 | – |
| local TEI (`gte-reranker-modernbert-base`) | 1.85 / 59% | 0.756 | 2.6 s |
| **Voyage `rerank-3`** | **1.97 / 68%** | **0.844** | 0.34 s |
| Voyage `rerank-3-lite` | 1.91 / 63% | 0.814 | 0.32 s |

The local re-ranker made the top 10 worse: nDCG@10 was better on 14 queries and worse on 26. Voyage `rerank-3` was
better on 26 and worse on 10. It is the re-ranker on Azure. Locally, without a Voyage key, the TEI re-ranker
remains as a fallback.

## Pool size: 50

#52 also re-ranked the same top 100 at several depths, with Voyage `rerank-3`:

| Re-ranked | nDCG@10 | Top 10: mean / rated ≥2 | Tokens per search (estimate) |
|---|---|---|---|
| top 10 | 0.807 | 1.86 / 60% | 1.7K |
| top 20 | 0.843 | 1.96 / 67% | 3.3K |
| top 30 | 0.844 | 1.97 / 68% | 5.0K |
| top 50 | 0.851 | 1.99 / 69% | 8.3K |
| top 100 | 0.852 | 1.99 / 69% | 16.7K |

Most of the gain comes by the top 20–30. #52 recommended 30, but the recommendation benchmark (#53) and the search
(#55) use a pool of 50. That is a small gain (+0.007 nDCG@10) for about 1.7× the re-ranker's tokens. On the
recommendation queries, that pipeline reached a top 10 with mean rating 2.08 and 81% relevant (#53).

## Query understanding: what retrieval and the re-ranker read (2026-10-31)

An LLM reads the query first: it extracts filters (first-published year and page bounds, only when the query states
them) and rewrites the query. Two choices were measured on the recommendation queries:
- what retrieval embeds;
- what the re-ranker reads.

Setup:
- the whole catalogue, a pool of 50, Voyage `rerank-3`;
- rewrites by `claude-haiku-5.5`, cached so every run compared the same ones;
- 2,371 new ratings (#70, closed: the benchmark tooling).

### What retrieval searches with (the pool of 50)

| Retrieval embeds | Relevant per query | Relevant share | Mean rating | Queries with ≥10 relevant |
|---|---|---|---|---|
| the query as typed | 29.1 | 58% | 1.70 | 66 |
| the LLM's description of the wanted book (a sentence) + filters | 30.6 | 61% | 1.74 | 66 |
| **the LLM's keywords for the wanted book + the same filters** | **33.1** | **66%** | **1.80** | **68** |

Per query, keywords beat the sentence on 37 queries, lose on 24 and tie on 9. The keywords are in the same shape
as the books' keywords (`enrich.QueryPrompt`).

Relevant works per query, by category (typed / sentence / keywords):

| Category | Typed | Sentence | Keywords |
|---|---|---|---|
| plot | 34.7 | 34.2 | **40.2** |
| vibe | 41.2 | 42.5 | **45.5** |
| like a book or author | 28.8 | 30.5 | **36.9** |
| character-driven | 30.5 | 31.0 | **32.4** |
| new experience | 20.8 | 21.2 | **22.5** |
| practical constraints | 27.8 | **35.1** | 34.9 |
| discovering something | **19.8** | **19.8** | 19.0 |

Keywords lead at every query length. In the pool, plot, mood and style score higher with keywords. The filters
improve era (1.24 → 1.39) and constraints (0.97 → 1.22).

### What the re-ranker reads (Voyage, on the sentence pool)

| Re-ranker reads | Mean rating | Rated ≥2 | Rated 3 | nDCG@10 | Pool use |
|---|---|---|---|---|---|
| **the query as typed** | **2.11** | 83% | 28% | **0.799** | **87%** |
| the LLM's description | 2.10 | 83% | 28% | 0.780 | 86% |

Pool use is the relevant works in the top 10 against the most the pool could put there. Per query, by nDCG@10, the
typed query wins 28 queries, the description wins 21, and 21 tie. The rewrite loses details the re-ranker uses:
- "nothing sad" (0.91 vs 0.67);
- "I need to laugh" (0.95 vs 0.79);
- "at her reading level" (0.88 vs 0.65).

### End to end (final top 10, Voyage reading the query as typed)

| Pool from | Mean rating | Rated ≥2 | Queries with ≥8 relevant | Pool use |
|---|---|---|---|---|
| the query as typed (before query understanding) | 2.08 | 81% | 48 | 84% |
| the LLM's description + filters | 2.11 | 83% | 54 | 87% |
| **the LLM's keywords + filters** | **2.15** | **87%** | **60** | **90%** |

### Decisions

- **Retrieval embeds the LLM's keywords, with its filters.** If the LLM fails or times out, retrieval uses the query
  as typed.
- **The re-ranker reads the query as typed.**
