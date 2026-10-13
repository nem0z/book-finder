package ingest

import (
	"context"
	"testing"
)

func TestParseRedirect(t *testing.T) {
	cases := []struct {
		key, location, from, to string
		fixed, ok               bool
	}{
		{"/works/OL2W", "/works/OL1W", "/works/OL2W", "/works/OL1W", false, true},
		{"/authors/OL9A", "OL1A", "/authors/OL9A", "/authors/OL1A", true, true},
		{"/books/OL2M", "/books/OL1M", "", "", false, false},
		{"/works/OL2W", "/books/OL1M", "", "", false, false},
		{"/works/OL2W", "OL1A", "", "", false, false},
		{"/authors/OL2W", "/authors/OL1A", "", "", false, false},
		{"/about/team", "/about/people", "", "", false, false},
		{"/works/OL2W", "", "", "", false, false},
	}

	for _, c := range cases {
		line := []byte("/type/redirect\t" + c.key + "\t1\t2026-09-30\t{\"location\": \"" + c.location + "\"}")
		from, to, fixed, ok := parseRedirect(line)
		if ok != c.ok || from != c.from || to != c.to || fixed != c.fixed {
			t.Errorf("%s -> %s: got %q %q %v %v", c.key, c.location, from, to, fixed, ok)
		}
	}
}

func TestResolve(t *testing.T) {
	l := newLoader(nil, Options{})
	l.redirects = map[string]string{
		"/works/OL1W": "/works/OL2W", "/works/OL2W": "/works/OL3W", // chain
		"/works/OL5W": "/works/OL6W", "/works/OL6W": "/works/OL5W", // loop
		"/authors/OL9A": "/authors/OL1A",
	}

	for in, want := range map[string]string{
		"/works/OL1W":   "/works/OL3W",
		"OL1W":          "/works/OL3W", // path-less ID in a record resolves like the full one
		"/works/OL3W":   "/works/OL3W",
		"/works/OL7W":   "/works/OL7W", // not redirected
		"OL9A":          "/authors/OL1A",
		"/about/team":   "",
		"/authors/OL9W": "",
	} {
		if got := l.resolve(in); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}

	if got := l.resolve("/works/OL5W"); got != "/works/OL5W" && got != "/works/OL6W" {
		t.Errorf("loop should stop after %d hops, got %q", maxRedirectHops, got)
	}
}

func TestLoadRedirects(t *testing.T) {
	l := newLoader(nil, Options{Sources: Sources{Redirects: redirectsFixture(t, t.TempDir())}, ProgressInterval: -1})
	if err := l.loadRedirects(context.Background()); err != nil {
		t.Fatal(err)
	}

	if s := l.stats.Redirects; s.Kept != 3 || s.FixedTargets != 1 || s.Skipped != 6 {
		t.Errorf("stats: %+v", s)
	}

	if got := l.resolve("/authors/OL9A"); got != "/authors/OL1A" {
		t.Errorf("fixed target: %q", got)
	}
}
