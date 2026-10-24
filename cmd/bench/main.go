// Command bench measures how well each search pipeline finds known books, and prints a Markdown
// report: see internal/bench. With -judge, it has an LLM rate the top 100 of each retrieval instead, without
// re-ranking (vector, keywords, and the vectors of the LLM keywords searched with the query or with the query's
// LLM keywords); the LLM is set by LLM_PROVIDER, LLM_URL, LLM_MODEL and LLM_API_KEY.
//
//	go run ./cmd/bench > bench.md
//	go run ./cmd/bench -sample -judge -o relevance.md
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/bench"
	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/embed"
	"github.com/nem0z/book-finder/internal/llm"
	"github.com/nem0z/book-finder/internal/rerank"
	"github.com/nem0z/book-finder/internal/search"
)

type config struct {
	url, ollama, queries, out string
	limit                     int
	sample                    bool
	rr                        search.Reranker

	judge, dryRun      bool
	cache, queryCache  string
	workers, perMinute int
}

func main() {
	var c config
	flag.StringVar(&c.url, "db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
	flag.StringVar(&c.ollama, "ollama", envOr("OLLAMA_URL", embed.DefaultOllamaURL), "Ollama URL (env OLLAMA_URL)")
	reranker := flag.String("reranker", envOr("RERANKER_URL", rerank.DefaultURL), "local re-ranker URL, used without VOYAGE_API_KEY (env RERANKER_URL)")
	voyageModel := flag.String("voyage-model", envOr("VOYAGE_MODEL", rerank.DefaultVoyageModel), "Voyage re-ranker, used when VOYAGE_API_KEY is set (env VOYAGE_MODEL)")
	noRerank := flag.Bool("no-rerank", false, "rank by retrieval only")
	flag.StringVar(&c.queries, "queries", "", "JSON file of queries (default: the built-in ones)")
	flag.IntVar(&c.limit, "n", search.DefaultLimit, "results per query")
	flag.StringVar(&c.out, "o", "", "write the report to this file instead of stdout")
	flag.BoolVar(&c.sample, "sample", false, "search only the works with LLM keywords, in every pipeline")
	flag.BoolVar(&c.judge, "judge", false, "have an LLM rate the top 100 of vector and keyword retrieval, without re-ranking")
	flag.StringVar(&c.cache, "judge-cache", "bench-judgments.jsonl", "file the -judge ratings are cached in")
	flag.BoolVar(&c.dryRun, "judge-dry-run", false, "with -judge, only count the ratings not cached yet")
	flag.StringVar(&c.queryCache, "query-keywords-cache", "bench-query-keywords.jsonl", "file the -judge query keywords are cached in")
	flag.IntVar(&c.workers, "concurrency", 4, "-judge ratings asked at once")
	flag.IntVar(&c.perMinute, "per-minute", 60, "at most this many -judge ratings per minute (0: no cap)")
	flag.Parse()

	if !*noRerank && !c.judge {
		var name string
		c.rr, name = rerank.New(rerank.Config{TEIURL: *reranker, VoyageAPIKey: os.Getenv("VOYAGE_API_KEY"), VoyageModel: *voyageModel})
		slog.Info("re-ranker", "name", name)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, c); err != nil {
		slog.Error("bench failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, c config) error {
	queries, err := bench.LoadQueries(c.queries)
	if err != nil {
		return err
	}

	pool, err := db.Connect(ctx, c.url, nil)
	if err != nil {
		return err
	}
	defer pool.Close()

	if _, err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	f, note, err := scope(ctx, pool, c.sample)
	if err != nil {
		return err
	}

	emb := embed.NewOllama(c.ollama, embed.DefaultModel)
	if c.judge {
		return runJudge(ctx, c, pool, emb, f, note, queries)
	}

	start := time.Now()
	rep, err := bench.Run(ctx, c.rr, pipelines(pool, emb, f), queries, c.limit)
	if err != nil {
		return err
	}

	rep.Note = note
	slog.Info("bench finished", "queries", len(queries), "took", time.Since(start).Round(time.Second))

	return write(c.out, rep.WriteMarkdown)
}

// runJudge has an LLM rate the top 100 of vector and keyword retrieval for every query.
func runJudge(ctx context.Context, c config, pool *pgxpool.Pool, emb search.QueryEmbedder, f search.Filters, note string, queries []bench.Query) error {
	lc := llm.Config{Provider: envOr("LLM_PROVIDER", llm.ProviderAzure), URL: os.Getenv("LLM_URL"),
		Model: os.Getenv("LLM_MODEL"), APIKey: os.Getenv("LLM_API_KEY")}
	client, err := llm.New(lc)
	if err != nil {
		return err
	}

	// The Copilot client runs a child process.
	if closer, ok := client.(io.Closer); ok {
		defer closer.Close()
	}

	judge, err := bench.OpenJudge(client, lc.Model, c.cache)
	if err != nil {
		return err
	}
	defer judge.Close()

	judge.Workers, judge.PerMinute, judge.DryRun = c.workers, c.perMinute, c.dryRun
	qk, err := bench.OpenQueryKeywords(client, lc.Model, c.queryCache)
	if err != nil {
		return err
	}
	defer qk.Close()

	keywordVectors := retriever(pool, emb, search.RetrieveKeywordVectors, f)
	methods := []bench.Method{
		{Name: "vector", Retrieve: retriever(pool, emb, search.RetrieveVector, f)},
		{Name: "keywords", Retrieve: retriever(pool, emb, search.RetrieveKeywords, f)},
		{Name: "keyword-vectors", Retrieve: keywordVectors},
		{
			Name: "keyword-vectors-llm",
			Prepare: func(ctx context.Context, query string) (time.Duration, error) {
				_, took, err := qk.Get(ctx, query)
				return took, err
			},
			Retrieve: func(ctx context.Context, query string, n int) ([]search.Result, error) {
				text, _, err := qk.Get(ctx, query)
				if err != nil {
					return nil, err
				}

				return keywordVectors(ctx, text, n)
			},
		},
	}

	start := time.Now()
	rel, err := bench.RunRelevance(ctx, judge, methods, queries)
	if err != nil {
		return err
	}

	if c.dryRun {
		slog.Info("dry run", "ratings to ask", judge.Pending)
		return nil
	}

	cached, asked, failed := judge.Stats()
	rel.Note = fmt.Sprintf("%s Ratings by %s (%s): %d cached, %d asked in this run, %d failed.", note, lc.Model,
		lc.Provider, cached, asked, failed)
	slog.Info("bench finished", "queries", len(queries), "took", time.Since(start).Round(time.Second))

	return write(c.out, rel.WriteMarkdown)
}

// write writes a report to the file at path, or to stdout when path is empty.
func write(path string, report func(io.Writer) error) error {
	if path == "" {
		return report(os.Stdout)
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}

	if err := report(out); err != nil {
		out.Close()
		return err
	}

	return out.Close()
}

// scope returns the filters every pipeline runs with, and a note on what they searched. With sample,
// they only search the works with LLM keywords, so that keyword retrieval can be compared before every
// work has keywords.
func scope(ctx context.Context, pool *pgxpool.Pool, sample bool) (search.Filters, string, error) {
	var vectors, keywords int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM work_embeddings), (SELECT count(*) FROM work_keywords)").
		Scan(&vectors, &keywords); err != nil {
		return search.Filters{}, "", err
	}

	if !sample {
		if keywords < vectors {
			return search.Filters{}, fmt.Sprintf("Keyword retrieval only searched the %d works with LLM keywords, "+
				"out of %d with a vector, so its results aren't comparable (see -sample).", keywords, vectors), nil
		}

		return search.Filters{}, "", nil
	}

	rows, err := pool.Query(ctx, "SELECT work_id FROM work_keywords")
	if err != nil {
		return search.Filters{}, "", err
	}

	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return search.Filters{}, "", err
	}

	if len(ids) == 0 {
		return search.Filters{}, "", errors.New("no work has LLM keywords yet; run cmd/enrich first")
	}

	return search.Filters{IDs: ids}, fmt.Sprintf("Every pipeline searched the same sample of %d works (those with LLM "+
		"keywords), not the %d with a vector. Results on a sample may not hold for the full catalogue, which has "+
		"more wrong candidates to rank above the target.", len(ids), vectors), nil
}

// pipelines returns the pipelines compared, all searching within f: vector search and keyword search on
// the LLM keywords, each re-ranking the default number of candidates or 100, and keyword search on the
// full text, re-ranking 100.
func pipelines(pool *pgxpool.Pool, emb search.QueryEmbedder, f search.Filters) []bench.Pipeline {
	retrieve := func(r search.Retrieval) bench.RetrieveFunc { return retriever(pool, emb, r, f) }

	return []bench.Pipeline{
		{Name: "vector", Retrieve: retrieve(search.RetrieveVector), Candidates: search.DefaultCandidates},
		{Name: "vector-100", Retrieve: retrieve(search.RetrieveVector), Candidates: bench.RecallDepth},
		{Name: "keywords", Retrieve: retrieve(search.RetrieveKeywords), Candidates: search.DefaultCandidates},
		{Name: "keywords-100", Retrieve: retrieve(search.RetrieveKeywords), Candidates: bench.RecallDepth},
		{Name: "fulltext-100", Retrieve: retrieve(search.RetrieveFullText), Candidates: bench.RecallDepth},
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

// retriever returns retrieval r within f.
func retriever(pool *pgxpool.Pool, emb search.QueryEmbedder, r search.Retrieval, f search.Filters) bench.RetrieveFunc {
	e := &search.Engine{Pool: pool, Embedder: emb, Retrieval: r}
	return func(ctx context.Context, query string, n int) ([]search.Result, error) {
		return e.Retrieve(ctx, query, f, n)
	}
}
