package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nem0z/book-finder/internal/ingest"
)

func TestCheckSources(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "dump.txt.gz")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := checkSources(ingest.Sources{Works: file, Editions: file, Authors: file}); err != nil {
		t.Errorf("redirects and ratings are optional: %v", err)
	}

	err := checkSources(ingest.Sources{Works: file, Editions: filepath.Join(dir, "missing.txt.gz"), Redirects: dir})
	if err == nil {
		t.Fatal("expected errors")
	}

	for _, want := range []string{"-editions:", "-authors is required", "-redirects:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}
