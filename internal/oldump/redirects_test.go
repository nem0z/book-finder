package oldump

import (
	"errors"
	"strconv"
	"testing"
)

func TestRedirectsAdd(t *testing.T) {
	cases := []struct {
		from, to string
		err      error  // nil when the redirect is stored
		want     string // Resolve(from) when stored
	}{
		{"/works/OL2W", "/works/OL1W", nil, "/works/OL1W"},
		{"/authors/OL9A", "OL1A", nil, "/authors/OL1A"}, // target without its path
		{"/authors/OL8A", "\t/authors/OL3A", nil, "/authors/OL3A"},
		{"/books/OL2M", "/books/OL1M", nil, "/books/OL1M"},
		{"/works/OL3W", "/books/OL1M", ErrKindMismatch, ""},
		{"/authors/OL2W", "/authors/OL1A", ErrInvalidKey, ""}, // letter does not match the path
		{"/authors/OL123", "/authors/OL1A", ErrInvalidKey, ""},
		{"/books/ia:cihm_1", "/books/OL3M", ErrInvalidKey, ""},
		{"/about/team", "/about/people", ErrInvalidKey, ""},
		{"/books/OL4M", "Diplomatarium Islandicum, Bd. 1", ErrInvalidKey, ""},
		{"/works/OL4W", "", ErrInvalidKey, ""},
		{"/works/OLxW", "/works/OL1W", ErrInvalidKey, ""},
		{"/works/OL05W", "/works/OL1W", ErrInvalidKey, ""},         // leading zero
		{"/works/OL+5W", "/works/OL1W", ErrInvalidKey, ""},         // sign
		{"/works/OL4294967296W", "/works/OL1W", ErrInvalidKey, ""}, // does not fit in 32 bits
	}

	for _, c := range cases {
		r := NewRedirects()
		err := r.Add(c.from, c.to)
		if !errors.Is(err, c.err) || (err != nil) != (c.err != nil) {
			t.Errorf("%q -> %q: got error %v, want %v", c.from, c.to, err, c.err)
			continue
		}

		if c.err != nil {
			if r.Len() != 0 {
				t.Errorf("%q -> %q: rejected but stored", c.from, c.to)
			}

			continue
		}

		if got, err := r.Resolve(c.from); err != nil || got != c.want {
			t.Errorf("%q -> %q: resolves to %q, %v", c.from, c.to, got, err)
		}
	}
}

func TestRedirectsResolve(t *testing.T) {
	r := NewRedirects()
	for from, to := range map[string]string{
		"/works/OL1W": "/works/OL2W", "/works/OL2W": "/works/OL3W", // chain
		"/works/OL5W": "/works/OL6W", "/works/OL6W": "/works/OL5W", // loop
		"/works/OL8W":   "/works/OL8W", // points to itself
		"/authors/OL9A": "OL1A",
	} {
		if err := r.Add(from, to); err != nil {
			t.Fatal(err)
		}
	}

	for in, want := range map[string]string{
		"/works/OL1W":   "/works/OL3W",
		"OL1W":          "/works/OL3W", // key without its path resolves like the full one
		" /works/OL3W ": "/works/OL3W",
		"/works/OL7W":   "/works/OL7W", // not redirected
		"/works/OL8W":   "/works/OL8W",
		"OL9A":          "/authors/OL1A",
		"/authors/OL1A": "/authors/OL1A",
		"/books/OL9M":   "/books/OL9M", // same number as a redirected author, different kind
	} {
		if got, err := r.Resolve(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v, want %q", in, got, err, want)
		}
	}

	for in, want := range map[string]error{
		"/works/OL5W":   ErrRedirectLoop,
		"/authors/OL1W": ErrInvalidKey, // OL1W is a work, not an author
		"/about/team":   ErrInvalidKey,
		"":              ErrInvalidKey,
	} {
		if got, err := r.Resolve(in); !errors.Is(err, want) {
			t.Errorf("%q: got %q, %v, want error %v", in, got, err, want)
		}
	}
}

func TestRedirectsMaxHops(t *testing.T) {
	r := NewRedirects()
	for i := range maxRedirectHops + 1 { // chain OL1W -> OL2W -> ... -> OL12W: 11 hops
		r.Add(workKey(i+1), workKey(i+2))
	}

	if got, err := r.Resolve(workKey(2)); err != nil || got != workKey(maxRedirectHops+2) {
		t.Errorf("%d hops: got %q, %v", maxRedirectHops, got, err)
	}

	if _, err := r.Resolve(workKey(1)); !errors.Is(err, ErrRedirectLoop) {
		t.Errorf("%d hops: got %v, want ErrRedirectLoop", maxRedirectHops+1, err)
	}
}

func workKey(n int) string { return WorksPath + idPrefix + strconv.Itoa(n) + "W" }
