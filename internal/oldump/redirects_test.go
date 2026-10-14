package oldump

import "testing"

func TestRedirectsAdd(t *testing.T) {
	cases := []struct {
		from, to string
		ok       bool
		want     string // Resolve(from), when ok
	}{
		{"/works/OL2W", "/works/OL1W", true, "/works/OL1W"},
		{"/authors/OL9A", "OL1A", true, "/authors/OL1A"}, // target without its path
		{"/authors/OL8A", "\t/authors/OL3A", true, "/authors/OL3A"},
		{"/books/OL2M", "/books/OL1M", true, "/books/OL1M"},
		{"/works/OL3W", "/books/OL1M", false, ""},     // different kinds
		{"/authors/OL2W", "/authors/OL1A", false, ""}, // letter does not match the path
		{"/authors/OL123", "/authors/OL1A", false, ""},
		{"/books/ia:cihm_1", "/books/OL3M", false, ""},
		{"/about/team", "/about/people", false, ""},
		{"/books/OL4M", "Diplomatarium Islandicum, Bd. 1", false, ""},
		{"/works/OL4W", "", false, ""},
		{"/works/OLxW", "/works/OL1W", false, ""},
		{"/works/OL05W", "/works/OL1W", false, ""},         // leading zero
		{"/works/OL+5W", "/works/OL1W", false, ""},         // sign
		{"/works/OL4294967296W", "/works/OL1W", false, ""}, // does not fit in 32 bits
	}

	for _, c := range cases {
		r := Redirects{}
		if ok := r.Add(c.from, c.to); ok != c.ok {
			t.Errorf("%q -> %q: got %v", c.from, c.to, ok)
			continue
		}

		if !c.ok && len(r) != 0 {
			t.Errorf("%q -> %q: rejected but stored %v", c.from, c.to, r)
		}

		if c.ok {
			if got := r.Resolve(c.from); got != c.want {
				t.Errorf("%q -> %q: resolves to %q", c.from, c.to, got)
			}
		}
	}
}

func TestRedirectsResolve(t *testing.T) {
	r := Redirects{}
	for from, to := range map[string]string{
		"/works/OL1W": "/works/OL2W", "/works/OL2W": "/works/OL3W", // chain
		"/works/OL5W": "/works/OL6W", "/works/OL6W": "/works/OL5W", // loop
		"/authors/OL9A": "OL1A",
	} {
		r.Add(from, to)
	}

	for in, want := range map[string]string{
		"/works/OL1W":   "/works/OL3W",
		"OL1W":          "/works/OL3W", // key without its path resolves like the full one
		" /works/OL3W ": "/works/OL3W",
		"/works/OL7W":   "/works/OL7W", // not redirected
		"OL9A":          "/authors/OL1A",
		"/authors/OL1A": "/authors/OL1A",
		"/authors/OL1W": "",            // OL1W is redirected as a work, not as an author
		"/books/OL9M":   "/books/OL9M", // same number as a redirected author, different kind
		"/about/team":   "",
		"":              "",
	} {
		if got := r.Resolve(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}

	if got := r.Resolve("/works/OL5W"); got != "/works/OL5W" && got != "/works/OL6W" {
		t.Errorf("loop should stop after %d hops, got %q", maxRedirectHops, got)
	}
}
