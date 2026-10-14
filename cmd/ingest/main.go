// Command ingest rebuilds the catalogue from Open Library dump files
// (https://openlibrary.org/developers/dumps). It applies the migrations first, so it can run on an
// empty database.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/ingest"
)

// bulkSessionParams speed up the load: the catalogue is rebuilt from the dumps, so losing the last
// commits in a crash costs nothing.
var bulkSessionParams = map[string]string{"synchronous_commit": "off"}

func main() {
	url := flag.String("db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
	var src ingest.Sources
	flag.StringVar(&src.Works, "works", "", "works dump (required)")
	flag.StringVar(&src.Editions, "editions", "", "editions dump (required)")
	flag.StringVar(&src.Authors, "authors", "", "authors dump (required)")
	flag.StringVar(&src.Redirects, "redirects", "", "redirects dump (optional; merged records are not resolved without it)")
	flag.StringVar(&src.Ratings, "ratings", "", "ratings dump (optional; works have no ratings without it)")
	workers := flag.Int("workers", 0, "JSON decode goroutines (default: number of CPUs)")
	flag.Parse()

	if err := checkSources(src); err != nil {
		fmt.Fprintln(os.Stderr, err)
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, *url, src, *workers); err != nil {
		slog.Error("ingest failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, url string, src ingest.Sources, workers int) error {
	pool, err := db.Connect(ctx, url, bulkSessionParams)
	if err != nil {
		return err
	}
	defer pool.Close()

	if _, err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	start := time.Now()
	stats, err := ingest.Run(ctx, pool, ingest.Options{Sources: src, Workers: workers})
	if err != nil {
		return err
	}

	out, err := json.Marshal(stats)
	if err != nil {
		return err
	}

	slog.Info("ingest done", "took", time.Since(start).Round(time.Second), "stats", string(out))

	return nil
}

// checkSources fails fast on a missing or mistyped file, rather than after the passes before it.
func checkSources(src ingest.Sources) error {
	files := []struct{ flag, path string }{
		{"works", src.Works}, {"editions", src.Editions}, {"authors", src.Authors},
		{"redirects", src.Redirects}, {"ratings", src.Ratings},
	}
	optional := map[string]bool{"redirects": true, "ratings": true}

	var errs []error
	for _, f := range files {
		if f.path == "" {
			if !optional[f.flag] {
				errs = append(errs, fmt.Errorf("-%s is required", f.flag))
			}

			continue
		}

		if info, err := os.Stat(f.path); err != nil {
			errs = append(errs, fmt.Errorf("-%s: %w", f.flag, err))
		} else if info.IsDir() {
			errs = append(errs, fmt.Errorf("-%s: %s is a directory", f.flag, f.path))
		}
	}

	return errors.Join(errs...)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
