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
	"github.com/nem0z/book-finder/internal/llm"
	"github.com/nem0z/book-finder/internal/rerank"
	"github.com/nem0z/book-finder/internal/search"
	"github.com/nem0z/book-finder/internal/understand"
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
	noRerank := flag.Bool("no-rerank", false, "keep the retrieval order")
	llmProvider := flag.String("llm", os.Getenv("LLM_PROVIDER"), "LLM that reads the query first: azure, ollama or copilot; empty: off (env LLM_PROVIDER, key in LLM_API_KEY)")
	llmURL := flag.String("llm-url", os.Getenv("LLM_URL"), "LLM API URL, for azure and ollama (env LLM_URL)")
	llmModel := flag.String("llm-model", os.Getenv("LLM_MODEL"), "LLM model, or deployment name on Azure (env LLM_MODEL)")
	llmTimeout := flag.String("llm-timeout", envOr("LLM_TIMEOUT", search.DefaultInterpretTimeout.String()), "time allowed to the LLM before searching with the query as is (env LLM_TIMEOUT)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	engine := &search.Engine{Embedder: embed.NewOllama(*ollama, embed.DefaultModel)}
	rrName := "off"
	if !*noRerank {
		engine.Reranker, rrName = rerank.New(rerank.Config{TEIURL: *reranker, VoyageAPIKey: os.Getenv("VOYAGE_API_KEY"), VoyageModel: *voyageModel})
	}

	it, timeout, err := understand.FromConfig(llm.Config{Provider: *llmProvider, URL: *llmURL, Model: *llmModel, APIKey: os.Getenv("LLM_API_KEY")}, *llmTimeout)
	if err != nil {
		slog.Error("bad LLM settings", "err", err)
		os.Exit(2)
	}

	llmName := "off"
	if it != nil {
		engine.Interpreter, engine.InterpretTimeout = it, timeout
		llmName = *llmProvider + " " + *llmModel
	}

	err = run(ctx, *addr, *url, engine, rrName, llmName)
	if cerr := it.Close(); cerr != nil {
		slog.Warn("LLM client not closed", "err", cerr)
	}

	if err != nil {
		slog.Error("api failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, addr, url string, engine *search.Engine, rrName, llmName string) error {
	pool, err := db.Connect(ctx, url, nil)
	if err != nil {
		return err
	}
	defer pool.Close()

	engine.Pool = pool

	srv := &http.Server{
		Addr:         addr,
		Handler:      api.NewHandler(engine, slog.Default()),
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	}

	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	slog.Info("listening", "addr", addr, "rerank", rrName, "llm", llmName)

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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
