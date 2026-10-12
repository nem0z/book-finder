// Command embed computes embeddings for works that are missing one or whose text changed.
//
// Requires a running Ollama server with the model pulled: `ollama pull embeddinggemma`.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/embed"
)

func main() {
	var (
		url     = flag.String("db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
		ollama  = flag.String("ollama", envOr("OLLAMA_URL", embed.DefaultOllamaURL), "Ollama server URL (env OLLAMA_URL)")
		model   = flag.String("model", envOr("EMBED_MODEL", embed.DefaultModel), "Ollama embedding model (env EMBED_MODEL)")
		batch   = flag.Int("batch", 32, "texts per Ollama request")
		workers = flag.Int("workers", 2, "concurrent Ollama requests")
		limit   = flag.Int("limit", 0, "embed at most N works, most-read first (0 = all)")
		all     = flag.Bool("all", false, "also embed works without a description")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	pool, err := db.Connect(ctx, *url, nil)
	if err != nil {
		fail("connect", err)
	}
	defer pool.Close()
	if _, err := db.Migrate(ctx, pool); err != nil {
		fail("migrate", err)
	}

	start := time.Now()
	stats, err := embed.Run(ctx, pool, embed.NewOllama(*ollama, *model), embed.Options{
		BatchSize: *batch, Workers: *workers, Limit: *limit, IncludeUndescribed: *all,
	})
	out, _ := json.MarshalIndent(stats, "", "  ")
	fmt.Println(string(out))
	if err != nil {
		fail("embed", err)
	}
	slog.Info("embedding complete", "took", time.Since(start).Round(time.Second))
}

func fail(step string, err error) {
	slog.Error(step, "err", err)
	os.Exit(1)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
