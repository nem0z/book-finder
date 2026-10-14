package ingest

import (
	"context"
	"testing"
)

func TestLoadRedirects(t *testing.T) {
	l := newLoader(nil, Options{Sources: Sources{Redirects: redirectsFixture(t, t.TempDir())}, ProgressInterval: -1})
	if err := l.loadRedirects(context.Background()); err != nil {
		t.Fatal(err)
	}

	if s := l.stats.Redirects; s.Kept != 4 || s.Skipped != 5 {
		t.Errorf("stats: %+v", s)
	}

	if got := l.redirects.Resolve("/authors/OL9A"); got != "/authors/OL1A" {
		t.Errorf("target without its path: %q", got)
	}
}
