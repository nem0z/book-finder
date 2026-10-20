// Command api serves the book search over HTTP: GET /search?q=<description of the book you want>
// returns the 10 best matches as JSON.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/nem0z/book-finder/internal/api"
	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/embed"
	"github.com/nem0z/book-finder/internal/rerank"
	"github.com/nem0z/book-finder/internal/search"
)

const (
	// A search takes a few seconds on CPU (embedding the query, then re-ranking).
	writeTimeout    = 30 * time.Second
	readTimeout     = 10 * time.Second
	shutdownTimeout = 10 * time.Second
)

func main() {
	addr := flag.String("addr", envOr("ADDR", "localhost:8080"), "listen address (env ADDR)")
	url := flag.String("db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
	ollama := flag.String("ollama", envOr("OLLAMA_URL", embed.DefaultOllamaURL), "Ollama URL (env OLLAMA_URL)")
	reranker := flag.String("reranker", envOr("RERANKER_URL", rerank.DefaultURL), "local re-ranker URL, used without VOYAGE_API_KEY (env RERANKER_URL)")
	voyageModel := flag.String("voyage-model", envOr("VOYAGE_MODEL", rerank.DefaultVoyageModel), "Voyage re-ranker, used when VOYAGE_API_KEY is set (env VOYAGE_MODEL)")
	noRerank := flag.Bool("no-rerank", false, "rank by vector similarity only")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var rr search.Reranker
	rrName := "off"
	if !*noRerank {
		rr, rrName = newReranker(*reranker, *voyageModel)
	}

	if err := run(ctx, *addr, *url, *ollama, rr, rrName); err != nil {
		slog.Error("api failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, addr, url, ollama string, rr search.Reranker, rrName string) error {
	pool, err := db.Connect(ctx, url, nil)
	if err != nil {
		return err
	}
	defer pool.Close()

	engine := &search.Engine{Pool: pool, Embedder: embed.NewOllama(ollama, embed.DefaultModel), Reranker: rr}

	srv := &http.Server{
		Addr:         addr,
		Handler:      api.NewHandler(engine, slog.Default()),
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	}

	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	slog.Info("listening", "addr", addr, "rerank", rrName)

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// newReranker returns Voyage's re-ranker when VOYAGE_API_KEY is set, else the local one at teiURL, and
// a name for the logs.
func newReranker(teiURL, voyageModel string) (search.Reranker, string) {
	if key := os.Getenv("VOYAGE_API_KEY"); key != "" {
		return rerank.NewVoyage(key, voyageModel), "voyage " + voyageModel
	}

	return rerank.NewTEI(teiURL), "tei " + teiURL
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
