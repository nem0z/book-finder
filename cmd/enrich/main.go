// Command enrich extracts keywords from the descriptions of the works with a vector, with an LLM, most
// rated first. It only extracts works whose keywords are missing or stale, so it can be stopped and run
// again. The LLM is set by LLM_PROVIDER (azure or ollama), LLM_URL, LLM_MODEL and LLM_API_KEY.
//
//	go run ./cmd/enrich -limit 300 -per-minute 30
package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/enrich"
	"github.com/nem0z/book-finder/internal/llm"
)

func main() {
	url := flag.String("db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
	limit := flag.Int("limit", 0, "only the N most-rated works with a vector (0: all)")
	ids := flag.String("ids", "", "only these works: comma-separated Open Library IDs, or @file with one per line")
	concurrency := flag.Int("concurrency", 4, "requests at once")
	perMinute := flag.Int("per-minute", 0, "at most this many requests per minute (0: no cap)")
	flag.Parse()

	c := llm.Config{Provider: envOr("LLM_PROVIDER", llm.ProviderAzure), URL: os.Getenv("LLM_URL"),
		Model: os.Getenv("LLM_MODEL"), APIKey: os.Getenv("LLM_API_KEY")}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	opts := enrich.Options{Limit: *limit, Concurrency: *concurrency, PerMinute: *perMinute}
	if err := run(ctx, *url, c, *ids, opts); err != nil {
		slog.Error("enrich failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, url string, c llm.Config, ids string, opts enrich.Options) error {
	client, err := llm.New(c)
	if err != nil {
		return err
	}

	// The Copilot client runs a child process.
	if closer, ok := client.(io.Closer); ok {
		defer closer.Close()
	}

	if opts.IDs, err = parseIDs(ids); err != nil {
		return err
	}

	pool, err := db.Connect(ctx, url, nil)
	if err != nil {
		return err
	}
	defer pool.Close()

	if _, err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	start := time.Now()
	stats, err := enrich.Run(ctx, pool, client, c.Model, opts)
	out, _ := json.Marshal(stats)
	slog.Info("enrich finished", "took", time.Since(start).Round(time.Second), "stats", string(out))

	return err
}

// parseIDs reads "OL1W,OL2W", or "@path" to a file with one ID per line.
func parseIDs(s string) ([]string, error) {
	if path, ok := strings.CutPrefix(s, "@"); ok {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}

		s = string(data)
	}

	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' }), nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
