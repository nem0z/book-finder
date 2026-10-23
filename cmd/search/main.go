// Command search runs a semantic search from the command line, for trying queries.
//
//	go run ./cmd/search -max-year 1980 "a melancholic story about memory and loss"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/embed"
	"github.com/nem0z/book-finder/internal/llm"
	"github.com/nem0z/book-finder/internal/rerank"
	"github.com/nem0z/book-finder/internal/search"
	"github.com/nem0z/book-finder/internal/understand"
)

const excerptBytes = 220

func main() {
	url := flag.String("db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
	ollama := flag.String("ollama", envOr("OLLAMA_URL", embed.DefaultOllamaURL), "Ollama URL (env OLLAMA_URL)")
	reranker := flag.String("reranker", envOr("RERANKER_URL", rerank.DefaultURL), "local re-ranker URL, used without VOYAGE_API_KEY (env RERANKER_URL)")
	voyageModel := flag.String("voyage-model", envOr("VOYAGE_MODEL", rerank.DefaultVoyageModel), "Voyage re-ranker, used when VOYAGE_API_KEY is set (env VOYAGE_MODEL)")
	noRerank := flag.Bool("no-rerank", false, "rank by vector similarity only")
	llmProvider := flag.String("llm", os.Getenv("LLM_PROVIDER"), "LLM that reads the query first: azure, ollama or copilot; empty: off (env LLM_PROVIDER, key in LLM_API_KEY)")
	llmURL := flag.String("llm-url", os.Getenv("LLM_URL"), "LLM API URL, for azure and ollama (env LLM_URL)")
	llmModel := flag.String("llm-model", os.Getenv("LLM_MODEL"), "LLM model, or deployment name on Azure (env LLM_MODEL)")
	llmTimeout := flag.String("llm-timeout", envOr("LLM_TIMEOUT", search.DefaultInterpretTimeout.String()), "time allowed to the LLM before searching with the query as is (env LLM_TIMEOUT)")
	noLLM := flag.Bool("no-llm", false, "search with the query as is")
	candidates := flag.Int("candidates", search.DefaultCandidates, "vector results passed to the re-ranker")
	limit := flag.Int("n", search.DefaultLimit, "number of results")
	asJSON := flag.Bool("json", false, "print the results as JSON")
	var f search.Filters
	flag.IntVar(&f.MinYear, "min-year", 0, "first published in or after this year")
	flag.IntVar(&f.MaxYear, "max-year", 0, "first published in or before this year")
	flag.IntVar(&f.MinPages, "min-pages", 0, "at least this many pages")
	flag.IntVar(&f.MaxPages, "max-pages", 0, "at most this many pages")
	flag.StringVar(&f.OriginalLanguage, "lang", "", "original language, MARC code (eng, fre, jpn...)")
	flag.Parse()

	query := strings.Join(flag.Args(), " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "usage: search [flags] <description of the book you want>")
		flag.PrintDefaults()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var rr search.Reranker
	if !*noRerank {
		rr, _ = rerank.New(rerank.Config{TEIURL: *reranker, VoyageAPIKey: os.Getenv("VOYAGE_API_KEY"), VoyageModel: *voyageModel})
	}

	e := search.Engine{Embedder: embed.NewOllama(*ollama, embed.DefaultModel), Reranker: rr, Candidates: *candidates}
	if !*noLLM {
		it, timeout, err := understand.FromConfig(llm.Config{Provider: *llmProvider, URL: *llmURL, Model: *llmModel, APIKey: os.Getenv("LLM_API_KEY")}, *llmTimeout)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer it.Close() //nolint:errcheck // exiting

		if it != nil {
			e.Interpreter, e.InterpretTimeout = it, timeout
		}
	}

	results, in, err := run(ctx, *url, &e, query, f, *limit)
	if err != nil {
		slog.Error("search failed", "err", err)
		os.Exit(1)
	}

	if *asJSON {
		out, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(out))
		return
	}

	if in != nil {
		printInterpretation(*in)
	}

	print(results)
}

// run finds the closest books by vector, then re-orders the best candidates when e has a re-ranker.
func run(ctx context.Context, url string, e *search.Engine, query string, f search.Filters, limit int) ([]search.Result, *search.Interpretation, error) {
	pool, err := db.Connect(ctx, url, nil)
	if err != nil {
		return nil, nil, err
	}
	defer pool.Close()

	e.Pool = pool

	return e.SearchInterpreted(ctx, query, f, limit)
}

// printInterpretation shows what the search embedded and the filters it applied.
func printInterpretation(in search.Interpretation) {
	fmt.Printf("Searched for: %s\n", in.Description)

	var filters []string
	f := in.Filters
	for _, b := range []struct {
		name string
		v    int
	}{{"min-year", f.MinYear}, {"max-year", f.MaxYear}, {"min-pages", f.MinPages}, {"max-pages", f.MaxPages}} {
		if b.v != 0 {
			filters = append(filters, fmt.Sprintf("%s %d", b.name, b.v))
		}
	}

	if f.OriginalLanguage != "" {
		filters = append(filters, "lang "+f.OriginalLanguage)
	}

	if len(filters) > 0 {
		fmt.Printf("Filters: %s\n", strings.Join(filters, ", "))
	}

	fmt.Println()
}

func print(results []search.Result) {
	if len(results) == 0 {
		fmt.Println("No results. Has `go run ./cmd/embed` been run?")
	}

	for i, r := range results {
		fmt.Printf("%2d. %s", i+1, r.Title)
		if len(r.Authors) > 0 {
			fmt.Printf(" by %s", strings.Join(r.Authors, ", "))
		}

		if r.Year > 0 {
			fmt.Printf(" (%d)", r.Year)
		}

		fmt.Printf("  [similarity %.2f", r.Similarity)
		if r.RerankScore != 0 {
			fmt.Printf(", re-rank %.2f", r.RerankScore)
		}

		fmt.Printf("]\n    %s\n    https://openlibrary.org/works/%s\n\n", excerpt(r.Description), r.ID)
	}
}

// excerpt returns the first line of s, cut at a word boundary.
func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= excerptBytes {
		return s
	}

	cut := strings.LastIndexByte(s[:excerptBytes], ' ')
	if cut <= 0 {
		cut = excerptBytes
	}

	return strings.ToValidUTF8(s[:cut], "") + "…"
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
