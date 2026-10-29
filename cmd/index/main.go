// Command index makes the catalogue searchable after cmd/ingest: an LLM extracts each eligible work's
// keywords and Ollama embeds them, most-rated first, in one streaming step (see internal/index). It only
// does what is missing or stale, so it can be stopped and run again, and after a reload it only does the
// new or edited works. The LLM is set by LLM_PROVIDER, LLM_URL, LLM_MODEL and LLM_API_KEY.
//
//	go run ./cmd/index -dry-run
//	go run ./cmd/index
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/embed"
	"github.com/nem0z/book-finder/internal/index"
	"github.com/nem0z/book-finder/internal/llm"
)

var errNoModel = errors.New("set LLM_MODEL: the model is part of the keywords' hash")

type config struct {
	url, ollama string
	opts        index.Options
	paced       *llm.Paced
}

func main() {
	c := config{paced: &llm.Paced{}}
	flag.StringVar(&c.url, "db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
	flag.StringVar(&c.ollama, "ollama", envOr("OLLAMA_URL", embed.DefaultOllamaURL), "Ollama URL (env OLLAMA_URL)")
	flag.IntVar(&c.opts.Limit, "limit", 0, "only the N most-rated eligible works, without pruning (0: all)")
	flag.BoolVar(&c.opts.DryRun, "dry-run", false, "only count what to extract and embed")
	flag.IntVar(&c.opts.Concurrency, "concurrency", 4, "LLM requests at once")
	flag.IntVar(&c.opts.PerRequest, "per-request", 5, "works per LLM request")
	flag.IntVar(&c.opts.Attempts, "attempts", 3, "rounds per work before it is left for the next run; the last asks for each work alone")
	flag.IntVar(&c.opts.EmbedBatch, "embed-batch", 32, "keyword texts per Ollama call")
	flag.IntVar(&c.paced.PerMinute, "per-minute", 60, "at most this many LLM requests per minute (0: no cap)")
	flag.DurationVar(&c.paced.Timeout, "timeout", 3*time.Minute, "per LLM request")
	flag.IntVar(&c.paced.Retries, "retries", 3, "tries again a rate-limited or timed-out LLM request this many times")
	flag.DurationVar(&c.paced.RestartEvery, "restart-every", 30*time.Minute, "restart the Copilot runtime this often (0: never)")
	flag.IntVar(&c.paced.RestartAfter, "restart-after", 5, "restart the Copilot runtime after this many LLM failures in a row (0: never)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, c); err != nil {
		slog.Error("index failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, c config) error {
	lc := llm.Config{Provider: envOr("LLM_PROVIDER", llm.ProviderAzure), URL: os.Getenv("LLM_URL"),
		Model: os.Getenv("LLM_MODEL"), APIKey: os.Getenv("LLM_API_KEY")}
	if lc.Model == "" {
		return errNoModel
	}

	client, err := llm.New(lc)
	if err != nil {
		return err
	}

	c.paced.Client, c.paced.Concurrency = client, c.opts.Concurrency
	defer c.paced.Close()

	pool, err := db.Connect(ctx, c.url, nil)
	if err != nil {
		return err
	}
	defer pool.Close()

	if _, err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	slog.Info("indexing", "llm", lc.Provider+" "+lc.Model, "embedding", embed.DefaultModel)
	start := time.Now()
	stats, err := index.Run(ctx, pool, c.paced, lc.Model, embed.NewOllama(c.ollama, embed.DefaultModel), c.opts)
	out, _ := json.Marshal(stats)
	slog.Info("index finished", "took", time.Since(start).Round(time.Second), "stats", string(out))

	return err
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
