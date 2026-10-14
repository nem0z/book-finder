// Command embed computes EmbeddingGemma vectors for the catalogue's works through Ollama, most-rated
// first. It only embeds works whose vector is missing or stale, so it can be stopped and run again.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/embed"
)

func main() {
	url := flag.String("db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
	ollama := flag.String("ollama", envOr("OLLAMA_URL", embed.DefaultOllamaURL), "Ollama URL (env OLLAMA_URL)")
	limit := flag.Int("limit", 0, "only the N most-rated eligible works (0: all)")
	minDesc := flag.Int("min-description", embed.DefaultMinDescription, "minimum description length, in bytes")
	batch := flag.Int("batch", 0, "texts per Ollama call (default 32)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, *url, *ollama, embed.Options{Limit: *limit, MinDescription: *minDesc, BatchSize: *batch}); err != nil {
		slog.Error("embed failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, url, ollama string, opts embed.Options) error {
	pool, err := db.Connect(ctx, url, nil)
	if err != nil {
		return err
	}
	defer pool.Close()

	if _, err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	start := time.Now()
	stats, err := embed.Run(ctx, pool, embed.NewOllama(ollama, embed.DefaultModel), opts)
	out, _ := json.Marshal(stats)
	slog.Info("embed finished", "took", time.Since(start).Round(time.Second), "stats", string(out))

	return err
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
