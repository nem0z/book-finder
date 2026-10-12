// Command migrate applies the database schema.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"

	"github.com/nem0z/book-finder/internal/db"
)

func main() {
	url := flag.String("db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	pool, err := db.Connect(ctx, *url, nil)
	if err != nil {
		slog.Error("connect", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	slog.Info("migrations applied", "versions", applied)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
