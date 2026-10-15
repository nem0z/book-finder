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
	"github.com/nem0z/book-finder/internal/search"
)

const excerptBytes = 220

func main() {
	url := flag.String("db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
	ollama := flag.String("ollama", envOr("OLLAMA_URL", embed.DefaultOllamaURL), "Ollama URL (env OLLAMA_URL)")
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

	results, err := run(ctx, *url, *ollama, query, f, *limit)
	if err != nil {
		slog.Error("search failed", "err", err)
		os.Exit(1)
	}

	if *asJSON {
		out, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(out))
		return
	}

	print(results)
}

func run(ctx context.Context, url, ollama, query string, f search.Filters, limit int) ([]search.Result, error) {
	pool, err := db.Connect(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	defer pool.Close()

	return search.Semantic(ctx, pool, embed.NewOllama(ollama, embed.DefaultModel), query, f, limit)
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

		fmt.Printf("  [similarity %.2f]\n    %s\n    https://openlibrary.org/works/%s\n\n", r.Similarity, excerpt(r.Description), r.ID)
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
