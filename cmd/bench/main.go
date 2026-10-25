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
	"slices"
	"strconv"
	"strings"
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
	rerankEffect       bool
	recommend          bool
	pool               int
	svg, rrName        string
	chunk, perMinuteRR int
	tokensPerMinute    int
	depths, scoreCache string
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
	flag.BoolVar(&c.recommend, "recommend", false, "compare description and keyword vectors, with and without re-ranking the top 30, on recommendation queries, with an LLM judging every result as a recommendation")
	flag.IntVar(&c.pool, "recommend-pool", 50, "with -recommend, how many keyword-vector results are re-ranked: the candidate pool")
	flag.BoolVar(&c.rerankEffect, "rerank-effect", false, "show what the re-ranker does to the top 100 of keyword-vector retrieval, re-ranking the top 30 or all 100, with -judge's ratings")
	flag.StringVar(&c.svg, "svg", "", "with -rerank-effect, also draw each variant as <svg>-<depth>.svg and the mean rating per rank as <svg>-curve.svg")
	flag.StringVar(&c.depths, "rerank-depths", "30,100", "with -rerank-effect, how many of the top 100 to re-rank, comma-separated; each work is scored once whatever the depths")
	flag.StringVar(&c.scoreCache, "rerank-cache", "bench-rerank-scores.jsonl", "with -rerank-effect, file the re-ranker's scores are cached in, by model")
	flag.IntVar(&c.chunk, "rerank-chunk", 0, "with -rerank-effect, at most this many texts per re-ranker call (0: no limit)")
	flag.IntVar(&c.perMinuteRR, "rerank-per-minute", 0, "with -rerank-effect, at most this many re-ranker calls per minute (0: no cap)")
	flag.IntVar(&c.tokensPerMinute, "rerank-tokens-per-minute", 0, "with -rerank-effect, at most about this many re-ranker tokens per minute (0: no cap)")
	flag.Parse()

	if !*noRerank && (!c.judge || c.rerankEffect || c.recommend) {
		c.rr, c.rrName = rerank.New(rerank.Config{TEIURL: *reranker, VoyageAPIKey: os.Getenv("VOYAGE_API_KEY"), VoyageModel: *voyageModel})
		slog.Info("re-ranker", "name", c.rrName)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, c); err != nil {
		slog.Error("bench failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, c config) error {
	load := bench.LoadQueries
	if c.recommend {
		load = bench.LoadRecommendationQueries
	}

	queries, err := load(c.queries)
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
	if c.recommend {
		return runRecommend(ctx, c, pool, emb, f, note, queries)
	}

	if c.rerankEffect {
		return runRerankEffect(ctx, c, pool, emb, f, note, queries)
	}

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

// openJudge returns the LLM set by LLM_PROVIDER, LLM_URL, LLM_MODEL and LLM_API_KEY, a judge asking it with
// c's settings, and a function that closes both.
func openJudge(c config) (llm.Client, llm.Config, *bench.Judge, func(), error) {
	return openJudgeRubric(c, bench.KnownItemRubric)
}

// openJudgeRubric is openJudge with another rubric.
func openJudgeRubric(c config, rubric bench.Rubric) (llm.Client, llm.Config, *bench.Judge, func(), error) {
	lc := llm.Config{Provider: envOr("LLM_PROVIDER", llm.ProviderAzure), URL: os.Getenv("LLM_URL"),
		Model: os.Getenv("LLM_MODEL"), APIKey: os.Getenv("LLM_API_KEY")}
	client, err := llm.New(lc)
	if err != nil {
		return nil, lc, nil, nil, err
	}

	// The Copilot client runs a child process.
	closeClient := func() {}
	if closer, ok := client.(io.Closer); ok {
		closeClient = func() { closer.Close() }
	}

	judge, err := bench.OpenJudgeRubric(client, lc.Model, c.cache, rubric)
	if err != nil {
		closeClient()
		return nil, lc, nil, nil, err
	}

	judge.Workers, judge.PerMinute, judge.DryRun = c.workers, c.perMinute, c.dryRun

	return client, lc, judge, func() { judge.Close(); closeClient() }, nil
}

// runRecommend evaluates the search's pipeline on the recommendation queries: keyword vectors, whose top
// c.pool (the candidate pool) is re-ranked. Description vectors are rated too, for comparison and to tell retrieval misses from catalogue
// gaps. Rating and re-ranking run at once; the report is written when the rating is done, without
// re-ranking, then again with it.
func runRecommend(ctx context.Context, c config, pool *pgxpool.Pool, emb search.QueryEmbedder, f search.Filters, note string, queries []bench.Query) error {
	_, lc, judge, closeAll, err := openJudgeRubric(c, bench.RecommendationRubric)
	if err != nil {
		return err
	}
	defer closeAll()

	start := time.Now()
	rec, err := bench.RetrieveRecommendations(ctx, []bench.Method{
		{Name: "description vectors", Retrieve: retriever(pool, emb, search.RetrieveVector, f)},
		{Name: "keyword vectors", Retrieve: retriever(pool, emb, search.RetrieveKeywordVectors, f)},
	}, queries)
	if err != nil {
		return err
	}

	if c.dryRun {
		if err := rec.Rate(ctx, judge); err != nil {
			return err
		}

		slog.Info("dry run", "ratings to ask", judge.Pending)
		return nil
	}

	type reranked struct {
		orders [][]search.Result
		took   []time.Duration
	}

	const keywordVectors = 1
	var res reranked
	done := make(chan error, 1)
	if c.rr != nil {
		rr := &bench.PacedReranker{RR: c.rr, Chunk: c.chunk, PerMinute: c.perMinuteRR, TokensPerMinute: c.tokensPerMinute}
		if err := rr.OpenCache(c.scoreCache, c.rrName); err != nil {
			return err
		}
		defer rr.Close()

		go func() {
			var err error
			res.orders, res.took, err = rec.RerankMethod(ctx, rr, keywordVectors, c.pool)
			done <- err
		}()
	} else {
		done <- nil
	}

	if err := rec.Rate(ctx, judge); err != nil {
		return err
	}

	cached, asked, failed := judge.Stats()
	rec.Note = fmt.Sprintf("%s Ratings by %s (%s), rubric %s: %d cached, %d asked in this run, %d failed.", note, lc.Model,
		lc.Provider, bench.RecommendationPromptVersion, cached, asked, failed)
	rec.Baseline, rec.Proposed, rec.Evaluated, rec.EvaluatedBase, rec.Pool = 0, keywordVectors, keywordVectors, -1, c.pool
	if err := write(c.out, rec.WriteMarkdown); err != nil {
		return err
	}

	slog.Info("rated; report written without re-ranking", "took", time.Since(start).Round(time.Second))
	if c.rr == nil {
		return nil
	}

	if err := <-done; err != nil {
		return err
	}

	rec.AddMethod(fmt.Sprintf("keyword vectors + %s top %d", c.rrName, c.pool), keywordVectors, res.orders, res.took)
	rec.Proposed, rec.Evaluated, rec.EvaluatedBase = 2, 2, keywordVectors
	slog.Info("bench finished", "queries", len(queries), "took", time.Since(start).Round(time.Second))

	return write(c.out, rec.WriteMarkdown)
}

// runRerankEffect re-ranks the top 30 and all 100 of keyword-vector retrieval for every query, and reports
// how the judge's ratings move.
func runRerankEffect(ctx context.Context, c config, pool *pgxpool.Pool, emb search.QueryEmbedder, f search.Filters, note string, queries []bench.Query) error {
	if c.rr == nil {
		return errors.New("-rerank-effect needs a re-ranker")
	}

	_, lc, judge, closeAll, err := openJudge(c)
	if err != nil {
		return err
	}
	defer closeAll()

	// Re-ranking the top 30 then all 100 scores each work once; 429s are retried.
	depths, err := parseDepths(c.depths)
	if err != nil {
		return err
	}

	rr := &bench.PacedReranker{RR: c.rr, Chunk: c.chunk, PerMinute: c.perMinuteRR, TokensPerMinute: c.tokensPerMinute}
	if err := rr.OpenCache(c.scoreCache, c.rrName); err != nil {
		return err
	}
	defer rr.Close()

	start := time.Now()
	e, err := bench.RunRerankEffect(ctx, judge, rr, retriever(pool, emb, search.RetrieveKeywordVectors, f), queries, depths)
	if err != nil {
		return err
	}

	if c.dryRun {
		slog.Info("dry run", "ratings to ask", judge.Pending)
		return nil
	}

	cached, asked, failed := judge.Stats()
	e.Note = fmt.Sprintf("%s Keyword-vector retrieval, query as typed, re-ranked by %s. Ratings by %s (%s): %d cached, %d "+
		"asked in this run, %d failed.", note, c.rrName, lc.Model, lc.Provider, cached, asked, failed)
	slog.Info("bench finished", "queries", len(queries), "took", time.Since(start).Round(time.Second))

	if c.svg != "" {
		for v, d := range depths {
			if !e.Detailed(v) {
				continue
			}

			if err := write(fmt.Sprintf("%s-%d.svg", c.svg, d), func(w io.Writer) error { return e.WriteSVG(w, v) }); err != nil {
				return err
			}
		}

		if err := write(c.svg+"-curve.svg", e.WriteCurveSVG); err != nil {
			return err
		}
	}

	return write(c.out, e.WriteMarkdown)
}

// runJudge has an LLM rate the top 100 of vector and keyword retrieval for every query.
func runJudge(ctx context.Context, c config, pool *pgxpool.Pool, emb search.QueryEmbedder, f search.Filters, note string, queries []bench.Query) error {
	client, lc, judge, closeAll, err := openJudge(c)
	if err != nil {
		return err
	}
	defer closeAll()
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

// parseDepths reads "30,100" as re-rank depths, each between 1 and 100. 30, as the search re-ranks, comes
// first, so that its re-rank time is that of one call rather than of the scores the other depths miss.
func parseDepths(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		d, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || d < 1 || d > bench.RecallDepth {
			return nil, fmt.Errorf("re-rank depth %q: want a number from 1 to %d", f, bench.RecallDepth)
		}

		out = append(out, d)
	}

	if i := slices.Index(out, search.DefaultCandidates); i > 0 {
		out = slices.Insert(slices.Delete(out, i, i+1), 0, search.DefaultCandidates)
	}

	return out, nil
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

		return search.Filters{}, fmt.Sprintf("Every method searched the whole catalogue: the %d works with a vector, all with LLM keywords.", vectors), nil
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
