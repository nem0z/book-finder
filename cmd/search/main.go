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

func main() {
	var (
		url    = flag.String("db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
		ollama = flag.String("ollama", envOr("OLLAMA_URL", embed.DefaultOllamaURL), "Ollama server URL (env OLLAMA_URL)")
		model  = flag.String("model", envOr("EMBED_MODEL", embed.DefaultModel), "Ollama embedding model (env EMBED_MODEL)")
		limit  = flag.Int("n", 10, "number of results")
		asJSON = flag.Bool("json", false, "print results as JSON")
		f      search.Filters
	)
	flag.IntVar(&f.MinYear, "min-year", 0, "first published in or after this year")
	flag.IntVar(&f.MaxYear, "max-year", 0, "first published in or before this year")
	flag.IntVar(&f.MinPages, "min-pages", 0, "at least this many pages")
	flag.IntVar(&f.MaxPages, "max-pages", 0, "at most this many pages")
	flag.StringVar(&f.OriginalLanguage, "lang", "", "original language, MARC code (eng, fre, jpn, ...)")
	flag.Parse()

	query := strings.Join(flag.Args(), " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "usage: search [flags] <description of the book you want>")
		flag.PrintDefaults()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	pool, err := db.Connect(ctx, *url, nil)
	if err != nil {
		fail("connect", err)
	}
	defer pool.Close()

	results, err := search.Semantic(ctx, pool, embed.NewOllama(*ollama, *model), query, f, *limit)
	if err != nil {
		fail("search", err)
	}
	if *asJSON {
		out, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(out))
		return
	}
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
		fmt.Printf("  [similarity %.2f]\n", r.Similarity)
		if d := excerpt(r.Description, 220); d != "" {
			fmt.Printf("    %s\n", d)
		}
		fmt.Printf("    https://openlibrary.org/works/%s\n\n", r.ID)
	}
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	if i := strings.LastIndexByte(s[:n], ' '); i > n/2 {
		n = i
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
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
