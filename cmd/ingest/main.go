// Command ingest rebuilds the catalogue from Open Library dump files.
//
// Point -dir at the folder holding the downloaded dumps (names like ol_dump_works_2026-09-30.txt.gz
// or ol_dump_works_latest.txt.gz), or pass each file explicitly.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"time"

	"github.com/nem0z/book-finder/internal/db"
	"github.com/nem0z/book-finder/internal/ingest"
)

func main() {
	var (
		url     = flag.String("db", envOr("DATABASE_URL", db.DefaultURL), "Postgres connection URL (env DATABASE_URL)")
		dir     = flag.String("dir", "", "directory containing ol_dump_<kind>_*.txt.gz files")
		workers = flag.Int("workers", 0, "JSON decode goroutines (default: number of CPUs)")
		src     ingest.Sources
	)
	flag.StringVar(&src.Works, "works", "", "works dump (required unless found in -dir)")
	flag.StringVar(&src.Editions, "editions", "", "editions dump (required unless found in -dir)")
	flag.StringVar(&src.Authors, "authors", "", "authors dump (required unless found in -dir)")
	flag.StringVar(&src.Ratings, "ratings", "", "ratings dump (optional)")
	flag.StringVar(&src.ReadingLog, "reading-log", "", "reading log dump (optional)")
	flag.StringVar(&src.Redirects, "redirects", "", "redirects dump (optional, recommended)")
	flag.Parse()

	if *dir != "" {
		for kind, p := range map[string]*string{
			"works": &src.Works, "editions": &src.Editions, "authors": &src.Authors,
			"ratings": &src.Ratings, "reading-log": &src.ReadingLog, "redirects": &src.Redirects,
		} {
			if *p == "" {
				*p = findDump(*dir, kind)
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Bulk-load settings for this session only.
	pool, err := db.Connect(ctx, *url, map[string]string{
		"synchronous_commit":   "off",
		"maintenance_work_mem": "1GB",
	})
	if err != nil {
		fail("connect", err)
	}
	defer pool.Close()

	if _, err := db.Migrate(ctx, pool); err != nil {
		fail("migrate", err)
	}

	start := time.Now()
	stats, err := ingest.Run(ctx, pool, ingest.Options{Sources: src, Workers: *workers})
	if err != nil {
		fail("ingest", err)
	}
	out, _ := json.MarshalIndent(stats, "", "  ")
	fmt.Println(string(out))
	slog.Info("ingest complete", "took", time.Since(start).Round(time.Second))
}

// findDump returns the newest ol_dump_<kind>_*.txt(.gz) in dir, or "" if none.
func findDump(dir, kind string) string {
	var matches []string
	for _, pattern := range []string{"ol_dump_" + kind + "_*.txt.gz", "ol_dump_" + kind + "_*.txt"} {
		m, _ := filepath.Glob(filepath.Join(dir, pattern))
		matches = append(matches, m...)
	}
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
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
