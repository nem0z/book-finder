// Command bench evaluates the search on queries asking for a kind of book, and writes a Markdown report: see
// internal/bench. It retrieves each query's top 100 as the search does, has an LLM rate them, re-ranks the
// first -candidates (the pool) with the search's re-ranker, and reports how relevant the pool and the final
// top 10 are. The LLM is set by LLM_PROVIDER, LLM_URL, LLM_MODEL and LLM_API_KEY.
//
//	go run ./cmd/bench -o bench.md
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"time"

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
	retrieval                 search.Retrieval
	candidates, depth         int

	rr                 search.Reranker
	rrName, scoreCache string
	chunk, perMinuteRR int
	tokensPerMinute    int

	cache              string
	dryRun             bool
	workers, perMinute int
}

// reranked is the outcome of re-ranking every pool.
type reranked struct {
	final [][]search.Result
	took  []time.Duration
	err   error
}

func main() {
	var c config
	flag.StringVar(&c.url, "db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
	flag.StringVar(&c.ollama, "ollama", envOr("OLLAMA_URL", embed.DefaultOllamaURL), "Ollama URL (env OLLAMA_URL)")
	retrieval := flag.String("retrieval", envOr("RETRIEVAL", string(search.RetrieveKeywordVectorsFirst)), "how candidates are found, as in cmd/search (env RETRIEVAL)")
	flag.IntVar(&c.candidates, "candidates", search.DefaultCandidates, "retrieved works re-ranked: the pool")
	flag.IntVar(&c.depth, "depth", bench.DefaultDepth, "retrieved works rated per query; those below the pool tell retrieval misses from catalogue gaps")
	reranker := flag.String("reranker", envOr("RERANKER_URL", rerank.DefaultURL), "local re-ranker URL, used without VOYAGE_API_KEY (env RERANKER_URL)")
	voyageModel := flag.String("voyage-model", envOr("VOYAGE_MODEL", rerank.DefaultVoyageModel), "Voyage re-ranker, used when VOYAGE_API_KEY is set (env VOYAGE_MODEL)")
	noRerank := flag.Bool("no-rerank", false, "evaluate the retrieval order")
	flag.StringVar(&c.scoreCache, "rerank-cache", "bench-rerank-scores.jsonl", "file the re-ranker's scores are cached in, by model")
	flag.IntVar(&c.chunk, "rerank-chunk", 0, "at most this many works per re-ranker call (0: the whole pool)")
	flag.IntVar(&c.perMinuteRR, "rerank-per-minute", 0, "at most this many re-ranker calls per minute (0: no cap)")
	flag.IntVar(&c.tokensPerMinute, "rerank-tokens-per-minute", 0, "at most about this many re-ranker tokens per minute (0: no cap)")
	flag.StringVar(&c.queries, "queries", "", "JSON file of queries (default: the built-in ones)")
	flag.StringVar(&c.out, "o", "", "write the report to this file instead of stdout")
	flag.StringVar(&c.cache, "judge-cache", "bench-judgments.jsonl", "file the ratings are cached in")
	flag.BoolVar(&c.dryRun, "dry-run", false, "only count the ratings not cached yet")
	flag.IntVar(&c.workers, "concurrency", 4, "ratings asked at once")
	flag.IntVar(&c.perMinute, "per-minute", 60, "at most this many ratings per minute (0: no cap)")
	flag.Parse()

	c.retrieval = search.Retrieval(*retrieval)
	if !*noRerank {
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

// run retrieves, then rates and re-ranks at once. The report is written when the rating is done, without
// re-ranking, then again with it.
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

	lc, judge, closeJudge, err := openJudge(c)
	if err != nil {
		return err
	}
	defer closeJudge()

	start := time.Now()
	e := &search.Engine{Pool: pool, Embedder: embed.NewOllama(c.ollama, embed.DefaultModel), Retrieval: c.retrieval}
	retrieve := func(ctx context.Context, query string, n int) ([]search.Result, error) {
		return e.Retrieve(ctx, query, search.Filters{}, n)
	}

	ev, err := bench.Retrieve(ctx, retrieve, queries, c.candidates, max(c.depth, c.candidates))
	if err != nil {
		return err
	}

	if c.dryRun {
		if err := ev.Rate(ctx, judge); err != nil {
			return err
		}

		slog.Info("dry run", "ratings to ask", judge.Pending)
		return nil
	}

	done := make(chan reranked, 1)
	if c.rr != nil {
		rr := &bench.PacedReranker{RR: c.rr, Chunk: c.chunk, PerMinute: c.perMinuteRR, TokensPerMinute: c.tokensPerMinute}
		if err := rr.OpenCache(c.scoreCache, c.rrName); err != nil {
			return err
		}
		defer rr.Close()

		// A copy, so that the report written meanwhile doesn't read what re-ranking writes.
		r := &bench.Eval{Queries: ev.Queries, Pool: ev.Pool, Retrieved: ev.Retrieved}
		go func() {
			err := r.RerankPool(ctx, rr)
			done <- reranked{r.Final, r.Rerank, err}
		}()
	}

	if err := ev.Rate(ctx, judge); err != nil {
		return err
	}

	cached, asked, failed := judge.Stats()
	note, err := describe(ctx, pool, c)
	if err != nil {
		return err
	}

	note += fmt.Sprintf(" Ratings by %s (%s), rubric %s: %d cached, %d asked in this run, %d failed.", lc.Model, lc.Provider,
		bench.PromptVersion, cached, asked, failed)
	ev.Note = note
	if c.rr != nil {
		ev.Note += " Re-ranking is still running: the top 10 is in retrieval order."
	}

	if err := write(c.out, ev.WriteMarkdown); err != nil {
		return err
	}

	if c.rr == nil {
		slog.Info("bench finished", "took", time.Since(start).Round(time.Second))
		return nil
	}

	slog.Info("rated; report written without re-ranking", "took", time.Since(start).Round(time.Second))
	res := <-done
	if res.err != nil {
		return res.err
	}

	ev.Final, ev.Rerank, ev.Note = res.final, res.took, note
	slog.Info("bench finished", "took", time.Since(start).Round(time.Second))

	return write(c.out, ev.WriteMarkdown)
}

// openJudge returns the LLM settings, a judge asking that LLM with c's settings, and a function that closes
// both.
func openJudge(c config) (llm.Config, *bench.Judge, func(), error) {
	lc := llm.Config{Provider: envOr("LLM_PROVIDER", llm.ProviderAzure), URL: os.Getenv("LLM_URL"),
		Model: os.Getenv("LLM_MODEL"), APIKey: os.Getenv("LLM_API_KEY")}
	client, err := llm.New(lc)
	if err != nil {
		return lc, nil, nil, err
	}

	// The Copilot client runs a child process.
	closeClient := func() {}
	if closer, ok := client.(io.Closer); ok {
		closeClient = func() { closer.Close() }
	}

	judge, err := bench.OpenJudge(client, lc.Model, c.cache)
	if err != nil {
		closeClient()
		return lc, nil, nil, err
	}

	judge.Workers, judge.PerMinute, judge.DryRun = c.workers, c.perMinute, c.dryRun

	return lc, judge, func() { judge.Close(); closeClient() }, nil
}

// describe says what was searched and how.
func describe(ctx context.Context, pool *pgxpool.Pool, c config) (string, error) {
	var vectors, keywordVectors int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM work_embeddings), (SELECT count(*) FROM work_keyword_embeddings)").
		Scan(&vectors, &keywordVectors); err != nil {
		return "", err
	}

	rr := "not re-ranked"
	if c.rr != nil {
		rr = "re-ranked by " + c.rrName
	}

	return fmt.Sprintf("Retrieval %s over %d works with a description vector, %d with a keyword vector; the top %d %s; "+
		"the top %d rated.", c.retrieval, vectors, keywordVectors, c.candidates, rr, max(c.depth, c.candidates)), nil
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
