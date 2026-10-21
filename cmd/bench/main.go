// Command bench measures how well each search pipeline finds known books, and prints a Markdown
// report: see internal/bench.
//
//	go run ./cmd/bench > bench.md
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/bench"
	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/embed"
	"github.com/nem0z/book-finder/internal/rerank"
	"github.com/nem0z/book-finder/internal/search"
)

type config struct {
	url, ollama, queries, out string
	limit                     int
	rr                        search.Reranker
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
	flag.Parse()

	if !*noRerank {
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

	start := time.Now()
	rep, err := bench.Run(ctx, c.rr, pipelines(pool, embed.NewOllama(c.ollama, embed.DefaultModel)), queries, c.limit)
	if err != nil {
		return err
	}

	slog.Info("bench finished", "queries", len(queries), "took", time.Since(start).Round(time.Second))

	if c.out == "" {
		return rep.WriteMarkdown(os.Stdout)
	}

	f, err := os.Create(c.out)
	if err != nil {
		return err
	}

	if err := rep.WriteMarkdown(f); err != nil {
		f.Close()
		return err
	}

	return f.Close()
}

// pipelines returns the pipelines compared: vector search re-ranking the default number of
// candidates, and re-ranking as many as the keyword pipeline will.
func pipelines(pool *pgxpool.Pool, emb search.QueryEmbedder) []bench.Pipeline {
	vector := func(ctx context.Context, query string, n int) ([]search.Result, error) {
		return search.Semantic(ctx, pool, emb, query, search.Filters{}, n)
	}

	return []bench.Pipeline{
		{Name: "vector", Retrieve: vector, Candidates: search.DefaultCandidates},
		{Name: "vector-100", Retrieve: vector, Candidates: bench.RecallDepth},
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
