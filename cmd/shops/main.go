// Command shops replaces the works of Chapters Bookstore with the books it lists now (see docs/shops.md). It
// stages the whole catalogue first, so a failed or interrupted run leaves the works untouched. It applies the
// migrations first, like cmd/ingest. Run cmd/index afterwards to index the new books.
//
//	go run ./cmd/shops -max-pages 2 # trial: reads 2 pages, changes nothing
//	go run ./cmd/shops
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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/shops"
)

// logEvery is how many pages pass between two progress lines (about 25 s at the client's pace).
const logEvery = 10

var errTrialDone = errors.New("trial pages read")

func main() {
	url := flag.String("db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
	maxPages := flag.Int("max-pages", 0, "trial: read only N pages of 250 products and leave the works unchanged (0: all)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, *url, shops.NewChapters(), *maxPages); err != nil {
		slog.Error("shops failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, url string, client *shops.Client, maxPages int) error {
	pool, err := db.Connect(ctx, url, nil)
	if err != nil {
		return err
	}
	defer pool.Close()

	if _, err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	return load(ctx, pool, client, maxPages)
}

// load stages the shop's books page by page, then replaces its works with them once every page was read.
// With maxPages > 0, it stops after that many pages and drops the stage.
func load(ctx context.Context, pool *pgxpool.Pool, client *shops.Client, maxPages int) error {
	stage, err := shops.NewStage(ctx, pool, client.Source)
	if err != nil {
		return err
	}
	defer func() {
		if err := stage.Drop(context.WithoutCancel(ctx)); err != nil {
			slog.Warn("drop stage", "err", err)
		}
	}()

	slog.Info("reading shop", "source", client.Source, "url", client.BaseURL)
	start := time.Now()
	pages := 0
	stats, err := client.Walk(ctx, func(books []shops.Book, s shops.Stats) error {
		if err := stage.Add(ctx, books); err != nil {
			return err
		}

		pages++
		if pages%logEvery == 0 {
			slog.Info("reading shop", "pages", pages, "books", s.Books, "products", s.Products, "retries", s.Retries,
				"took", time.Since(start).Round(time.Second))
		}

		if maxPages > 0 && pages >= maxPages {
			return errTrialDone
		}

		return nil
	})
	out, _ := json.Marshal(stats)
	took := time.Since(start).Round(time.Second)
	if errors.Is(err, errTrialDone) {
		slog.Info("trial done, works unchanged", "pages", pages, "took", took, "stats", string(out))
		return nil
	}

	if err != nil {
		slog.Info("shop read stopped, works unchanged", "pages", pages, "took", took, "stats", string(out))
		return err
	}

	r, err := stage.Replace(ctx)
	if err != nil {
		return err
	}

	slog.Info("shops done", "source", client.Source, "pages", pages, "deleted", r.Deleted, "inserted", r.Inserted,
		"took", time.Since(start).Round(time.Second), "stats", string(out))

	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
