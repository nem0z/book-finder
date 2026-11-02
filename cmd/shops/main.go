// Command shops replaces the works of each bookshop in shops.All with the books it lists now (see docs/shops.md).
// It stages a shop's whole catalogue first, so a failed or interrupted run leaves that shop's works untouched. It
// applies the migrations first, like cmd/ingest. Run cmd/index afterwards to index the new books.
//
//	go run ./cmd/shops -shop chapters -max-pages 2 # trial: reads 2 pages, changes nothing
//	go run ./cmd/shops                             # every shop, one after the other
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
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/shops"
	"github.com/nem0z/book-finder/internal/shops/shopify"
)

// logEvery is how many pages pass between two progress lines (about 25 s at the client's pace).
const logEvery = 10

var errTrialDone = errors.New("trial pages read")

func main() {
	url := flag.String("db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
	shop := flag.String("shop", "all", "shop to load: "+strings.Join(shopIDs(), ", ")+", or all")
	maxPages := flag.Int("max-pages", 0, "trial: read only N pages of products and leave the works unchanged (0: all)")
	flag.Parse()

	list, err := selectShops(*shop)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, *url, list, *maxPages); err != nil {
		slog.Error("shops failed", "err", err)
		os.Exit(1)
	}
}

// run loads each shop in turn; one failing does not stop the others.
func run(ctx context.Context, url string, list []shops.Shop, maxPages int) error {
	pool, err := db.Connect(ctx, url, nil)
	if err != nil {
		return err
	}
	defer pool.Close()

	if _, err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	var errs []error
	for _, s := range list {
		r, err := reader(s, shops.NewClient())
		if err == nil {
			err = load(ctx, pool, r, maxPages)
		}

		if err != nil {
			slog.Error("shop failed", "shop", s.ID, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", s.ID, err))
		}

		if ctx.Err() != nil {
			break
		}
	}

	return errors.Join(errs...)
}

// selectShops returns the shop named by the -shop flag, or every shop for "all".
func selectShops(id string) ([]shops.Shop, error) {
	if id == "all" {
		return shops.All, nil
	}

	s, err := shops.Find(id)
	if err != nil {
		return nil, err
	}

	return []shops.Shop{s}, nil
}

// reader returns a reader of the shop's catalog on its platform.
func reader(s shops.Shop, client *shops.Client) (shops.Reader, error) {
	r := shops.Reader{Shop: s, Client: client}
	switch s.Platform {
	case shops.Shopify:
		r.Catalog = shopify.New(s, client)
	default:
		return r, fmt.Errorf("shop %s: unknown platform %q", s.ID, s.Platform)
	}

	return r, nil
}

func shopIDs() []string {
	ids := make([]string, len(shops.All))
	for i, s := range shops.All {
		ids[i] = s.ID
	}

	return ids
}

// load stages the shop's books page by page, then replaces its works with them once every page was read.
// With maxPages > 0, it stops after that many pages and drops the stage.
func load(ctx context.Context, pool *pgxpool.Pool, r shops.Reader, maxPages int) error {
	stage, err := shops.NewStage(ctx, pool, r.Shop.ID)
	if err != nil {
		return err
	}
	defer func() {
		if err := stage.Drop(context.WithoutCancel(ctx)); err != nil {
			slog.Warn("drop stage", "err", err)
		}
	}()

	slog.Info("reading shop", "shop", r.Shop.ID, "url", r.Shop.URL)
	start := time.Now()
	pages := 0
	stats, err := r.Walk(ctx, func(books []shops.Book, s shops.Stats) error {
		if err := stage.Add(ctx, books); err != nil {
			return err
		}

		pages++
		if pages%logEvery == 0 {
			slog.Info("reading shop", "shop", r.Shop.ID, "pages", pages, "books", s.Books, "products", s.Products, "retries", s.Retries,
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
		slog.Info("trial done, works unchanged", "shop", r.Shop.ID, "pages", pages, "took", took, "stats", string(out))
		return nil
	}

	if err != nil {
		slog.Info("shop read stopped, works unchanged", "shop", r.Shop.ID, "pages", pages, "took", took, "stats", string(out))
		return err
	}

	replaced, err := stage.Replace(ctx)
	if err != nil {
		return err
	}

	slog.Info("shop done", "shop", r.Shop.ID, "pages", pages, "deleted", replaced.Deleted, "inserted", replaced.Inserted,
		"took", time.Since(start).Round(time.Second), "stats", string(out))

	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
